package ws

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"zenmind-voice-server/internal/asr"
	"zenmind-voice-server/internal/config"
	"zenmind-voice-server/internal/core"
	"zenmind-voice-server/internal/runner"
	"zenmind-voice-server/internal/tts"
)

const proxyClientQueueFullMessage = "Client events queued before upstream ready exceeded limit"
const taskLimitExceededMessage = "Active task limit reached for this connection"

type Handler struct {
	app        *config.App
	upstream   asr.RealtimeUpstreamGateway
	ttsService *tts.SynthesisService
	runner     runner.Client
	upgrader   websocket.Upgrader
	sessions   sync.Map // sessionID(string) -> *sessionContext
	draining   atomic.Bool
}

func NewHandler(app *config.App, upstream asr.RealtimeUpstreamGateway, ttsService *tts.SynthesisService, runnerClient runner.Client) *Handler {
	return &Handler{
		app:        app,
		upstream:   upstream,
		ttsService: ttsService,
		runner:     runnerClient,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return app.WS.IsOriginAllowed(r.Header.Get("Origin"))
			},
		},
	}
}

// IsDraining 返回 Handler 是否进入了优雅关闭流程，用于 readiness 探针判断。
func (h *Handler) IsDraining() bool {
	return h.draining.Load()
}

// Shutdown 通知所有活跃会话进入 draining 状态：先发 connection.draining 事件，
// 给客户端 ctx 截止时间内的宽限期，到期后强制关闭所有连接。
func (h *Handler) Shutdown(ctx context.Context) error {
	if !h.draining.CompareAndSwap(false, true) {
		return nil
	}
	graceMs := 0
	if deadline, ok := ctx.Deadline(); ok {
		graceMs = int(time.Until(deadline) / time.Millisecond)
		if graceMs < 0 {
			graceMs = 0
		}
	}
	h.sessions.Range(func(_, v any) bool {
		sc := v.(*sessionContext)
		sc.sendJSON(map[string]any{
			"type":      "connection.draining",
			"sessionId": sc.sessionID,
			"graceMs":   graceMs,
		})
		return true
	})

	// 等到 ctx 截止或所有会话已自然关闭
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if h.activeSessions() == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			h.sessions.Range(func(_, v any) bool {
				sc := v.(*sessionContext)
				sc.closeDone()
				_ = sc.conn.Close()
				return true
			})
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (h *Handler) activeSessions() int {
	count := 0
	h.sessions.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}

func (h *Handler) pongTimeout() time.Duration {
	if h.app.WS.PongTimeoutMs <= 0 {
		return 60 * time.Second
	}
	return time.Duration(h.app.WS.PongTimeoutMs) * time.Millisecond
}

func (h *Handler) pingInterval() time.Duration {
	if h.app.WS.PingIntervalMs <= 0 {
		return 30 * time.Second
	}
	return time.Duration(h.app.WS.PingIntervalMs) * time.Millisecond
}

func (h *Handler) writeTimeout() time.Duration {
	if h.app.WS.WriteTimeoutMs <= 0 {
		return 10 * time.Second
	}
	return time.Duration(h.app.WS.WriteTimeoutMs) * time.Millisecond
}

func (h *Handler) maxMessageBytes() int64 {
	if h.app.WS.MaxMessageBytes <= 0 {
		return 2097152
	}
	return h.app.WS.MaxMessageBytes
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Warn("ws upgrade failed", "err", err)
		return
	}

	session := newSessionContext(conn, r.RemoteAddr, h.writeTimeout(), h.app.WS.OutboundQueueSize)

	conn.SetReadLimit(h.maxMessageBytes())
	pongWait := h.pongTimeout()
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	go session.writerLoop()
	go h.pingLoop(session)

	h.sessions.Store(session.sessionID, session)

	h.logSessionEvent(session, "connection.open")
	session.sendJSON(map[string]any{
		"type":            "connection.ready",
		"sessionId":       session.sessionID,
		"protocolVersion": "v2",
		"capabilities": map[string]any{
			"asr":             true,
			"tts":             true,
			"ttsModes":        []string{"local", "llm"},
			"streamInput":     true,
			"deprecatedModes": []string{"llm"},
		},
	})

	for {
		messageType, payload, err := conn.ReadMessage()
		if err != nil {
			h.cleanup(session, false)
			return
		}

		if messageType == websocket.BinaryMessage {
			h.sendError(session, "", "bad_request", "Binary websocket frames are not supported from client")
			continue
		}
		if messageType != websocket.TextMessage {
			continue
		}
		h.handleTextMessage(session, payload)
	}
}

func (h *Handler) pingLoop(session *sessionContext) {
	ticker := time.NewTicker(h.pingInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if !session.sendPing(h.writeTimeout()) {
				return
			}
		case <-session.doneCh:
			return
		}
	}
}

type clientEvent struct {
	Type          string          `json:"type"`
	TaskID        string          `json:"taskId"`
	SampleRate    int             `json:"sampleRate"`
	Language      string          `json:"language"`
	Audio         string          `json:"audio"`
	ClientGate    json.RawMessage `json:"clientGate"`
	TurnDetection json.RawMessage `json:"turnDetection"`
	Mode          string          `json:"mode"`
	Text          string          `json:"text"`
	Voice         string          `json:"voice"`
	ChatID        string          `json:"chatId"`
	AgentKey      string          `json:"agentKey"`
	InputMode     string          `json:"inputMode"`
	SpeechRate    *float64        `json:"speechRate"`
}

func (h *Handler) handleTextMessage(session *sessionContext, payload []byte) {
	var event clientEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		h.sendError(session, "", "bad_request", "Payload is not valid JSON")
		return
	}
	if strings.TrimSpace(event.Type) == "" {
		h.sendError(session, "", "bad_request", "Payload must be a JSON object")
		return
	}

	switch event.Type {
	case "asr.start":
		h.handleAsrStart(session, event)
	case "asr.audio.append":
		h.handleAsrAudioAppend(session, event, payload)
	case "asr.audio.commit":
		h.handleAsrAudioCommit(session, event)
	case "asr.stop":
		h.handleAsrStop(session, event)
	case "tts.start":
		h.handleTtsStart(session, event)
	case "tts.append":
		h.handleTtsAppend(session, event)
	case "tts.commit":
		h.handleTtsCommit(session, event)
	case "tts.stop":
		h.handleTtsStop(session, event)
	default:
		h.sendError(session, event.TaskID, "bad_request", "Unsupported event type: "+event.Type)
	}
}

func (h *Handler) handleAsrStart(session *sessionContext, event clientEvent) {
	taskID := strings.TrimSpace(event.TaskID)
	if taskID == "" {
		h.sendError(session, "", "bad_request", "taskId must not be blank")
		return
	}
	switch session.reserveTaskID(taskID, h.app.WS.MaxTasksPerConn) {
	case reserveConflict:
		h.sendError(session, taskID, "task_conflict", "taskId is already active")
		return
	case reserveLimitExceeded:
		h.sendError(session, taskID, "task_limit_exceeded", taskLimitExceededMessage)
		return
	}
	if !h.app.Asr.Realtime.HasAPIKey() {
		session.releaseTaskID(taskID)
		h.sendError(session, taskID, "config_missing", "ASR realtime API key is missing")
		return
	}

	task := &asrTask{
		taskID:                 taskID,
		sampleRate:             defaultInt(event.SampleRate, 16000),
		language:               defaultString(event.Language, "zh"),
		pendingClientEvents:    make([]queuedEvent, 0, 8),
		seenUpstreamEventTypes: make(map[string]struct{}),
	}
	task.turnDetectionPayload = buildAsrSessionUpdatePayload(h.app.Asr.TurnDetection.Normalized(), event.TurnDetection, task.sampleRate, task.language)
	session.setAsrTask(taskID, task)
	h.logTaskEvent("asr", session.sessionID, taskID, "asr.start",
		detailField("sample_rate", task.sampleRate),
		detailField("language", task.language),
		detailField("client_gate", compactJSON(event.ClientGate)),
		detailField("turn_detection", compactJSON(event.TurnDetection)),
	)

	go h.connectUpstream(session, task)
}

func (h *Handler) handleAsrAudioAppend(session *sessionContext, event clientEvent, originalPayload []byte) {
	taskID := strings.TrimSpace(event.TaskID)
	if taskID == "" {
		h.sendError(session, "", "bad_request", "taskId must not be blank")
		return
	}
	task := session.getAsrTask(taskID)
	if task == nil {
		h.sendError(session, taskID, "task_not_found", "ASR task is not active")
		return
	}
	audio, decodedBytes, ok := h.validateAudioAppend(session, taskID, event, originalPayload)
	if !ok {
		return
	}
	h.logTaskEvent("asr", session.sessionID, taskID, "asr.audio.append",
		detailField("payload_bytes", len(originalPayload)),
		detailField("audio_base64_chars", len(audio)),
		detailField("audio_bytes", decodedBytes),
	)
	h.dispatchClientEvent(session, task, queuedEvent{
		payload:      fmt.Sprintf(`{"type":"input_audio_buffer.append","audio":"%s"}`, audio),
		payloadBytes: len(originalPayload),
	})
}

func (h *Handler) handleAsrAudioCommit(session *sessionContext, event clientEvent) {
	taskID := strings.TrimSpace(event.TaskID)
	if taskID == "" {
		h.sendError(session, "", "bad_request", "taskId must not be blank")
		return
	}
	task := session.getAsrTask(taskID)
	if task == nil {
		h.sendError(session, taskID, "task_not_found", "ASR task is not active")
		return
	}
	h.logTaskEvent("asr", session.sessionID, taskID, "asr.audio.commit")
	h.dispatchClientEvent(session, task, queuedEvent{
		payload:      `{"type":"input_audio_buffer.commit"}`,
		payloadBytes: 36,
	})
}

func (h *Handler) handleAsrStop(session *sessionContext, event clientEvent) {
	taskID := strings.TrimSpace(event.TaskID)
	if taskID == "" {
		h.sendError(session, "", "bad_request", "taskId must not be blank")
		return
	}
	task := session.getAsrTask(taskID)
	if task == nil {
		h.sendError(session, taskID, "task_not_found", "ASR task is not active")
		return
	}
	h.logTaskEvent("asr", session.sessionID, taskID, "asr.stop")
	h.finishAsrTask(session, task, "client_stop", true, true)
}

func (h *Handler) handleTtsStart(session *sessionContext, event clientEvent) {
	taskID := strings.TrimSpace(event.TaskID)
	if taskID == "" {
		h.sendError(session, "", "bad_request", "taskId must not be blank")
		return
	}
	switch session.reserveTaskID(taskID, h.app.WS.MaxTasksPerConn) {
	case reserveConflict:
		h.sendError(session, taskID, "task_conflict", "taskId is already active")
		return
	case reserveLimitExceeded:
		h.sendError(session, taskID, "task_limit_exceeded", taskLimitExceededMessage)
		return
	}

	mode := defaultString(event.Mode, h.app.Tts.DefaultMode)
	inputMode := defaultString(event.InputMode, "single")
	text := strings.TrimSpace(event.Text)
	if mode != "local" && mode != "llm" {
		session.releaseTaskID(taskID)
		h.sendError(session, taskID, "bad_request", "Unsupported tts mode: "+mode)
		return
	}
	if inputMode != "single" && inputMode != "stream" {
		session.releaseTaskID(taskID)
		h.sendError(session, taskID, "bad_request", "Unsupported tts inputMode: "+inputMode)
		return
	}
	if mode == "llm" && inputMode != "single" {
		session.releaseTaskID(taskID)
		h.sendError(session, taskID, "bad_request", "tts.start llm mode only supports single inputMode")
		return
	}
	if inputMode == "single" && text == "" {
		session.releaseTaskID(taskID)
		h.sendError(session, taskID, "bad_request", "tts.start requires non-empty text")
		return
	}
	if !h.ttsService.IsLocalConfigured() {
		session.releaseTaskID(taskID)
		h.sendError(session, taskID, "config_missing", "Local TTS API key is missing")
		return
	}
	if mode == "llm" && !h.app.Tts.Llm.Runner.IsConfigured() {
		session.releaseTaskID(taskID)
		h.sendError(session, taskID, "config_missing", "Runner SSE is not configured")
		return
	}
	resolvedAgentKey := strings.TrimSpace(event.AgentKey)
	if resolvedAgentKey == "" {
		resolvedAgentKey = strings.TrimSpace(h.app.Tts.Llm.Runner.AgentKey)
	}
	if mode == "llm" && resolvedAgentKey == "" {
		session.releaseTaskID(taskID)
		h.sendError(session, taskID, "bad_request", "tts.start requires agentKey for llm mode")
		return
	}

	plan, err := h.ttsService.OpenSession(event.Voice, event.SpeechRate)
	if err != nil {
		session.releaseTaskID(taskID)
		h.sendLoggedError(session, taskID, "tts", "tts_failed", err.Error(), err, "")
		return
	}

	task := &ttsTask{
		taskID:    taskID,
		mode:      mode,
		inputMode: inputMode,
		text:      text,
		chatID:    strings.TrimSpace(event.ChatID),
		agentKey:  resolvedAgentKey,
		plan:      plan,
	}
	session.setTtsTask(taskID, task)
	resolvedSpeechRate := h.app.Tts.Local.SpeechRate
	if event.SpeechRate != nil {
		resolvedSpeechRate = *event.SpeechRate
	}
	logFields := []slog.Attr{
		detailField("mode", mode),
		detailField("input_mode", inputMode),
		detailField("voice", task.plan.VoiceID),
		detailField("speech_rate", resolvedSpeechRate),
	}
	if task.text != "" {
		logFields = append(logFields, detailField("text", task.text))
	}
	if task.mode == "llm" {
		logFields = append(logFields,
			detailField("chat_id", task.chatID),
			detailField("agent_key", task.agentKey),
		)
	}
	h.logTaskEvent("tts", session.sessionID, taskID, "tts.start", logFields...)
	h.startTtsTask(session, task)
}

func (h *Handler) handleTtsAppend(session *sessionContext, event clientEvent) {
	taskID := strings.TrimSpace(event.TaskID)
	if taskID == "" {
		h.sendError(session, "", "bad_request", "taskId must not be blank")
		return
	}
	task := session.getTtsTask(taskID)
	if task == nil {
		h.sendError(session, taskID, "task_not_found", "TTS task is not active")
		return
	}
	if task.mode != "local" || task.inputMode != "stream" {
		h.sendError(session, taskID, "bad_request", "tts.append requires an active local stream task")
		return
	}
	text := event.Text
	if strings.TrimSpace(text) == "" {
		h.sendError(session, taskID, "bad_request", "tts.append requires non-empty text")
		return
	}
	h.logTaskEvent("tts", session.sessionID, taskID, "tts.append", detailField("text", text))
	task.hasContent.Store(true)
	task.plan.Session.AppendText(text)
}

func (h *Handler) handleTtsCommit(session *sessionContext, event clientEvent) {
	taskID := strings.TrimSpace(event.TaskID)
	if taskID == "" {
		h.sendError(session, "", "bad_request", "taskId must not be blank")
		return
	}
	task := session.getTtsTask(taskID)
	if task == nil {
		h.sendError(session, taskID, "task_not_found", "TTS task is not active")
		return
	}
	if task.mode != "local" || task.inputMode != "stream" {
		h.sendError(session, taskID, "bad_request", "tts.commit requires an active local stream task")
		return
	}
	if task.committed.CompareAndSwap(false, true) {
		h.logTaskEvent("tts", session.sessionID, taskID, "tts.commit")
		if !task.hasContent.Load() {
			session.sendJSON(eventBody("tts.done", session.sessionID, task.taskID, map[string]any{"reason": "no_content"}))
			h.logTaskEvent("tts", session.sessionID, task.taskID, "tts.done", detailField("reason", "no_content"))
			h.finishTtsTask(session, task, "no_content", true, true)
			return
		}
		task.plan.Session.Finish()
	}
}

func (h *Handler) handleTtsStop(session *sessionContext, event clientEvent) {
	taskID := strings.TrimSpace(event.TaskID)
	if taskID == "" {
		h.sendError(session, "", "bad_request", "taskId must not be blank")
		return
	}
	task := session.getTtsTask(taskID)
	if task == nil {
		h.sendError(session, taskID, "task_not_found", "TTS task is not active")
		return
	}
	h.logTaskEvent("tts", session.sessionID, taskID, "tts.stop")
	session.sendJSON(eventBody("tts.done", session.sessionID, taskID, map[string]any{"reason": "client_stop"}))
	h.logTaskEvent("tts", session.sessionID, taskID, "tts.done", detailField("reason", "client_stop"))
	h.finishTtsTask(session, task, "client_stop", true, true)
}

func (h *Handler) connectUpstream(session *sessionContext, task *asrTask) {
	ctx := context.Background()
	upstreamSession, err := h.upstream.Connect(ctx, session.sessionID+":"+task.taskID, asr.ConnectOptions{}, &upstreamListener{
		onMessage: func(payload string) {
			h.forwardNormalizedAsrEvent(session, task, payload)
		},
		onClose: func(_ int, reason string) {
			if strings.TrimSpace(reason) == "" {
				reason = "upstream_closed"
			}
			h.logTaskEvent("asr", session.sessionID, task.taskID, "upstream.closed", detailField("reason", reason))
			h.finishAsrTask(session, task, reason, false, true)
		},
		onError: func(err error) {
			h.logTaskEvent("asr", session.sessionID, task.taskID, "upstream.error", detailField("cause", err.Error()))
			h.sendLoggedError(session, task.taskID, "asr", "upstream_error", "Upstream realtime service error", err, "")
			h.finishAsrTask(session, task, "upstream_error", false, true)
		},
	})
	if err != nil {
		h.logTaskEvent("asr", session.sessionID, task.taskID, "upstream.connect_failed", detailField("cause", err.Error()))
		h.sendLoggedError(session, task.taskID, "asr", "upstream_connect_failed", "Failed to connect upstream realtime service", err, "")
		h.finishAsrTask(session, task, "connect_failed", false, true)
		return
	}
	h.logTaskEvent("asr", session.sessionID, task.taskID, "upstream.connected")

	task.mu.Lock()
	if task.stopped.Load() || session.closed.Load() {
		task.mu.Unlock()
		_ = upstreamSession.Close(websocket.CloseNormalClosure, "task already stopped")
		return
	}
	task.upstream = upstreamSession
	task.upstreamReady = true
	task.mu.Unlock()

	if err := upstreamSession.SendText(task.turnDetectionPayload); err != nil {
		h.sendLoggedError(session, task.taskID, "asr", "upstream_error", "Upstream realtime service is not available", err, task.turnDetectionPayload)
		h.finishAsrTask(session, task, "upstream_error", false, true)
		return
	}
	session.sendJSON(eventBody("task.started", session.sessionID, task.taskID, map[string]any{"taskType": "asr"}))
	h.logTaskEvent("asr", session.sessionID, task.taskID, "task.started")
	h.flushPendingClientEvents(session, task)
}

func (h *Handler) startTtsTask(session *sessionContext, task *ttsTask) {
	session.sendJSON(eventBody("task.started", session.sessionID, task.taskID, map[string]any{
		"taskType":  "tts",
		"mode":      task.mode,
		"inputMode": task.inputMode,
	}))
	h.logTaskEvent("tts", session.sessionID, task.taskID, "task.started")
	session.sendJSON(eventBody("tts.audio.format", session.sessionID, task.taskID, map[string]any{
		"sampleRate":       task.plan.SampleRate,
		"channels":         task.plan.Channels,
		"voice":            task.plan.VoiceID,
		"voiceDisplayName": task.plan.VoiceDisplayName,
	}))
	h.logTaskEvent("tts", session.sessionID, task.taskID, "tts.audio.format",
		detailField("sample_rate", task.plan.SampleRate),
		detailField("channels", task.plan.Channels),
		detailField("voice", task.plan.VoiceID),
		detailField("voice_display_name", compactVoiceDisplayName(task.plan.VoiceID, task.plan.VoiceDisplayName)),
	)

	go h.streamTtsAudio(session, task)
	if task.mode == "local" {
		if task.text != "" {
			task.hasContent.Store(true)
			task.plan.Session.AppendText(task.text)
		}
		if task.inputMode == "single" {
			task.committed.Store(true)
			task.plan.Session.Finish()
		}
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	task.runnerCancel = cancel
	eventCh, errCh := h.runner.StreamEvents(ctx, task.text, task.chatID, task.agentKey)
	go func() {
		for {
			select {
			case event, ok := <-eventCh:
				if !ok {
					eventCh = nil
					if !task.hasContent.Load() {
						session.sendJSON(eventBody("tts.done", session.sessionID, task.taskID, map[string]any{"reason": "no_content"}))
						h.logTaskEvent("tts", session.sessionID, task.taskID, "tts.done", detailField("reason", "no_content"))
						h.finishTtsTask(session, task, "no_content", true, true)
						return
					}
					task.plan.Session.Finish()
					continue
				}
				if event.IsChatUpdated() {
					task.chatID = event.ChatID
					session.sendJSON(eventBody("tts.chat.updated", session.sessionID, task.taskID, map[string]any{
						"chatId": event.ChatID,
					}))
					h.logTaskEvent("tts", session.sessionID, task.taskID, "tts.chat.updated", detailField("chat_id", event.ChatID))
					continue
				}
				if !event.IsContentDelta() || strings.TrimSpace(event.Delta) == "" {
					continue
				}
				task.hasContent.Store(true)
				session.sendJSON(eventBody("tts.text.delta", session.sessionID, task.taskID, map[string]any{
					"text": event.Delta,
				}))
				h.logTaskEvent("tts", session.sessionID, task.taskID, "tts.text.delta", detailField("text", event.Delta))
				task.plan.Session.AppendText(event.Delta)
			case err, ok := <-errCh:
				if !ok {
					errCh = nil
					if eventCh == nil {
						return
					}
					continue
				}
				if err == nil {
					continue
				}
				h.sendLoggedError(session, task.taskID, "tts", "runner_failed", err.Error(), err, "")
				h.finishTtsTask(session, task, "runner_failed", true, true)
				return
			}
		}
	}()
}

func (h *Handler) streamTtsAudio(session *sessionContext, task *ttsTask) {
	audioCh := task.plan.Session.AudioChan()
	errCh := task.plan.Session.ErrChan()
	doneCh := task.plan.Session.DoneChan()
	for {
		select {
		case chunk, ok := <-audioCh:
			if !ok {
				audioCh = nil
				continue
			}
			seq := task.audioSequence.Add(1)
			h.logTaskEvent("tts", session.sessionID, task.taskID, "tts.audio.chunk",
				detailField("seq", seq),
				detailField("audio_bytes", len(chunk.PCM16LE)),
			)
			session.sendTtsChunkPair(task.taskID, int(seq), chunk)
		case err, ok := <-errCh:
			if !ok {
				errCh = nil
				continue
			}
			if err == nil {
				continue
			}
			h.sendLoggedError(session, task.taskID, "tts", "tts_failed", err.Error(), err, "")
			h.finishTtsTask(session, task, "tts_failed", true, true)
			return
		case <-doneCh:
			if task.stopped.Load() {
				return
			}
			session.sendJSON(eventBody("tts.done", session.sessionID, task.taskID, map[string]any{"reason": "completed"}))
			h.logTaskEvent("tts", session.sessionID, task.taskID, "tts.done", detailField("reason", "completed"))
			h.finishTtsTask(session, task, "completed", false, true)
			return
		}
	}
}

func (h *Handler) forwardNormalizedAsrEvent(session *sessionContext, task *asrTask, payload string) {
	var event map[string]any
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		return
	}
	eventType, _ := event["type"].(string)

	task.mu.Lock()
	if eventType != "" {
		task.seenUpstreamEventTypes[eventType] = struct{}{}
	}
	task.mu.Unlock()

	if eventType == "error" {
		code := nestedString(event, "error", "code")
		if code == "" {
			code = "upstream_error"
		}
		message := nestedString(event, "error", "message")
		if message == "" {
			message = "Upstream error"
		}
		h.logTaskEvent("asr", session.sessionID, task.taskID, "upstream.error",
			detailField("code", code),
			detailField("message", message),
		)
		h.sendLoggedError(session, task.taskID, "asr", code, message, nil, payload)
		h.finishAsrTask(session, task, code, false, true)
		return
	}

	if deltaText := extractDeltaText(event); strings.TrimSpace(deltaText) != "" {
		session.sendJSON(eventBody("asr.text.delta", session.sessionID, task.taskID, map[string]any{
			"text":         deltaText,
			"upstreamType": eventType,
		}))
		h.logTaskEvent("asr", session.sessionID, task.taskID, "asr.text.delta",
			detailField("text", deltaText),
			detailField("upstream_type", eventType),
		)
	}
	if finalText := extractFinalText(event); strings.TrimSpace(finalText) != "" {
		session.sendJSON(eventBody("asr.text.final", session.sessionID, task.taskID, map[string]any{
			"text":         finalText,
			"upstreamType": eventType,
		}))
		h.logTaskEvent("asr", session.sessionID, task.taskID, "asr.text.final",
			detailField("text", finalText),
			detailField("upstream_type", eventType),
		)
	}
	if eventType == "input_audio_buffer.speech_started" {
		session.sendJSON(eventBody("asr.speech.started", session.sessionID, task.taskID, map[string]any{
			"upstreamType": eventType,
		}))
		h.logTaskEvent("asr", session.sessionID, task.taskID, "asr.speech.started", detailField("upstream_type", eventType))
	}
	if eventType == "session.finished" {
		h.finishAsrTask(session, task, "upstream_finished", false, true)
	}
}

func (h *Handler) validateAudioAppend(session *sessionContext, taskID string, event clientEvent, originalPayload []byte) (string, int, bool) {
	realtime := h.app.Asr.Realtime
	if len(originalPayload) > realtime.MaxClientEventBytes {
		h.sendError(session, taskID, "event_too_large", "Client event exceeds maximum size")
		return "", 0, false
	}
	audio := strings.TrimSpace(event.Audio)
	if audio == "" {
		h.sendError(session, taskID, "bad_request", "audio.append requires non-empty string field 'audio'")
		return "", 0, false
	}
	if len(audio) > realtime.MaxAppendAudioChars {
		h.sendError(session, taskID, "audio_too_large", "Audio payload exceeds maximum size")
		return "", 0, false
	}
	decoded, err := base64.StdEncoding.DecodeString(audio)
	if err != nil {
		h.sendError(session, taskID, "bad_request", "audio must be valid base64 pcm16le")
		return "", 0, false
	}
	return audio, len(decoded), true
}

func (h *Handler) dispatchClientEvent(session *sessionContext, task *asrTask, event queuedEvent) {
	task.mu.Lock()
	defer task.mu.Unlock()
	if task.upstreamReady && task.upstream != nil && task.upstream.IsOpen() {
		if err := task.upstream.SendText(event.payload); err != nil {
			h.sendLoggedError(session, task.taskID, "asr", "upstream_error", "Upstream realtime service is not available", err, event.payload)
			go h.finishAsrTask(session, task, "upstream_error", false, true)
		}
		return
	}

	nextEvents := len(task.pendingClientEvents) + 1
	nextBytes := task.pendingClientEventBytes + event.payloadBytes
	if nextEvents > h.app.Asr.Realtime.MaxPendingClientEvents || nextBytes > h.app.Asr.Realtime.MaxPendingClientBytes {
		h.sendError(session, task.taskID, "proxy_client_queue_full", proxyClientQueueFullMessage)
		go h.finishAsrTask(session, task, "proxy_client_queue_full", false, true)
		return
	}
	task.pendingClientEvents = append(task.pendingClientEvents, event)
	task.pendingClientEventBytes = nextBytes
}

func (h *Handler) flushPendingClientEvents(session *sessionContext, task *asrTask) {
	for {
		task.mu.Lock()
		if len(task.pendingClientEvents) == 0 {
			task.pendingClientEventBytes = 0
			task.mu.Unlock()
			return
		}
		event := task.pendingClientEvents[0]
		task.pendingClientEvents = task.pendingClientEvents[1:]
		task.pendingClientEventBytes -= event.payloadBytes
		upstream := task.upstream
		task.mu.Unlock()

		if upstream == nil || !upstream.IsOpen() {
			h.sendLoggedError(session, task.taskID, "asr", "upstream_error", "Upstream realtime service is not available", nil, event.payload)
			h.finishAsrTask(session, task, "upstream_error", false, true)
			return
		}
		if err := upstream.SendText(event.payload); err != nil {
			h.sendLoggedError(session, task.taskID, "asr", "upstream_error", "Upstream realtime service is not available", err, event.payload)
			h.finishAsrTask(session, task, "upstream_error", false, true)
			return
		}
	}
}

func (h *Handler) finishAsrTask(session *sessionContext, task *asrTask, reason string, closeUpstream bool, notify bool) {
	if !task.stopped.CompareAndSwap(false, true) {
		return
	}
	session.deleteAsrTask(task.taskID)
	session.releaseTaskID(task.taskID)

	task.mu.Lock()
	upstream := task.upstream
	task.upstream = nil
	task.upstreamReady = false
	task.pendingClientEvents = nil
	task.pendingClientEventBytes = 0
	task.mu.Unlock()

	if closeUpstream && upstream != nil {
		_ = upstream.Close(websocket.CloseNormalClosure, reason)
	}
	if notify {
		session.sendJSON(eventBody("task.stopped", session.sessionID, task.taskID, map[string]any{
			"taskType": "asr",
			"reason":   reason,
		}))
	}
	h.logTaskEvent("asr", session.sessionID, task.taskID, "task.stopped", detailField("reason", reason))
}

func (h *Handler) finishTtsTask(session *sessionContext, task *ttsTask, reason string, cancelSession bool, notify bool) {
	if !task.stopped.CompareAndSwap(false, true) {
		return
	}
	session.deleteTtsTask(task.taskID)
	session.releaseTaskID(task.taskID)
	if task.runnerCancel != nil {
		task.runnerCancel()
	}
	if cancelSession && task.plan.Session != nil {
		task.plan.Session.Cancel()
	}
	if notify {
		session.sendJSON(eventBody("task.stopped", session.sessionID, task.taskID, map[string]any{
			"taskType": "tts",
			"reason":   reason,
		}))
	}
	h.logTaskEvent("tts", session.sessionID, task.taskID, "task.stopped", detailField("reason", reason))
}

func (h *Handler) cleanup(session *sessionContext, notify bool) {
	if !session.closed.CompareAndSwap(false, true) {
		return
	}
	h.sessions.Delete(session.sessionID)
	session.closeDone()
	closeReason := "connection_closed"
	if session.overflow.Load() {
		closeReason = "outbound_overflow"
		notify = false
	}
	if h.draining.Load() {
		closeReason = "draining"
	}
	h.logSessionEvent(session, "connection.closed", detailField("reason", closeReason))
	for _, task := range session.listAsrTasks() {
		h.finishAsrTask(session, task, closeReason, true, notify)
	}
	for _, task := range session.listTtsTasks() {
		h.finishTtsTask(session, task, closeReason, true, notify)
	}
	_ = session.conn.Close()
}

func (h *Handler) sendError(session *sessionContext, taskID, code, message string) {
	session.sendJSON(eventBody("error", session.sessionID, taskID, map[string]any{
		"code":    code,
		"message": message,
	}))
}

func (h *Handler) sendLoggedError(session *sessionContext, taskID, taskType, code, message string, cause error, upstreamPayload string) {
	attrs := []slog.Attr{
		slog.String("component", taskType),
		slog.String("session_id", session.sessionID),
		slog.String("task_id", taskID),
		slog.String("code", code),
		slog.String("message", message),
	}
	if cause != nil {
		attrs = append(attrs, slog.String("cause", cause.Error()))
	}
	if strings.TrimSpace(upstreamPayload) != "" {
		attrs = append(attrs, slog.Int("upstream_payload_bytes", len(upstreamPayload)))
	}
	slog.LogAttrs(context.Background(), slog.LevelError, "voice.error", attrs...)
	h.sendError(session, taskID, code, message)
}

func (h *Handler) logSessionEvent(session *sessionContext, event string, fields ...slog.Attr) {
	baseFields := append([]slog.Attr{slog.String("remote_addr", session.remoteAddr)}, fields...)
	if h.app.Asr.WebSocketDetailedLogEnabled {
		h.logTaskEvent("asr", session.sessionID, "", event, baseFields...)
	}
	if h.app.Tts.WebSocketDetailedLogEnabled {
		h.logTaskEvent("tts", session.sessionID, "", event, baseFields...)
	}
}

func (h *Handler) logTaskEvent(taskType, sessionID, taskID, event string, attrs ...slog.Attr) {
	if !h.shouldLogDetailed(taskType) {
		return
	}
	base := []slog.Attr{
		slog.String("component", taskType),
		slog.String("session_id", sessionID),
	}
	if strings.TrimSpace(taskID) != "" {
		base = append(base, slog.String("task_id", taskID))
	}
	base = append(base, attrs...)
	slog.LogAttrs(context.Background(), slog.LevelInfo, event, filterEmptyAttrs(base)...)
}

func (h *Handler) shouldLogDetailed(taskType string) bool {
	switch taskType {
	case "asr":
		return h.app.Asr.WebSocketDetailedLogEnabled
	case "tts":
		return h.app.Tts.WebSocketDetailedLogEnabled
	default:
		return false
	}
}

type outboundMessage struct {
	// 普通 JSON 事件（与 pair* 互斥）
	json map[string]any
	// TTS 配对帧：先写 header JSON，紧接着写 binary，保证不被打散
	pairHeader map[string]any
	pairBinary []byte
}

type sessionContext struct {
	conn         *websocket.Conn
	sessionID    string
	remoteAddr   string
	writeTimeout time.Duration
	outboundCh   chan outboundMessage
	taskMu       sync.Mutex
	taskIDs      map[string]struct{}
	asrTasks     map[string]*asrTask
	ttsTasks     map[string]*ttsTask
	closed       atomic.Bool
	overflow     atomic.Bool
	doneCh       chan struct{}
	doneOnce     sync.Once
}

func newSessionContext(conn *websocket.Conn, remoteAddr string, writeTimeout time.Duration, queueSize int) *sessionContext {
	if queueSize <= 0 {
		queueSize = 64
	}
	return &sessionContext{
		conn:         conn,
		sessionID:    fmt.Sprintf("ws-session-%d", time.Now().UnixNano()),
		remoteAddr:   strings.TrimSpace(remoteAddr),
		writeTimeout: writeTimeout,
		outboundCh:   make(chan outboundMessage, queueSize),
		taskIDs:      make(map[string]struct{}),
		asrTasks:     make(map[string]*asrTask),
		ttsTasks:     make(map[string]*ttsTask),
		doneCh:       make(chan struct{}),
	}
}

func (s *sessionContext) closeDone() {
	s.doneOnce.Do(func() { close(s.doneCh) })
}

func (s *sessionContext) writerLoop() {
	for {
		select {
		case msg := <-s.outboundCh:
			if !s.writeOutbound(msg) {
				s.closeDone()
				return
			}
		case <-s.doneCh:
			return
		}
	}
}

func (s *sessionContext) writeOutbound(msg outboundMessage) bool {
	if s.writeTimeout > 0 {
		_ = s.conn.SetWriteDeadline(time.Now().Add(s.writeTimeout))
	}
	if msg.pairBinary != nil {
		if err := s.conn.WriteJSON(msg.pairHeader); err != nil {
			return false
		}
		if s.writeTimeout > 0 {
			_ = s.conn.SetWriteDeadline(time.Now().Add(s.writeTimeout))
		}
		if err := s.conn.WriteMessage(websocket.BinaryMessage, msg.pairBinary); err != nil {
			return false
		}
		return true
	}
	if err := s.conn.WriteJSON(msg.json); err != nil {
		return false
	}
	return true
}

func (s *sessionContext) sendJSON(payload map[string]any) {
	if s.closed.Load() {
		return
	}
	select {
	case s.outboundCh <- outboundMessage{json: payload}:
	case <-s.doneCh:
	default:
		s.markOverflow()
	}
}

func (s *sessionContext) sendTtsChunkPair(taskID string, seq int, chunk core.AudioChunk) {
	if s.closed.Load() {
		return
	}
	header := eventBody("tts.audio.chunk", s.sessionID, taskID, map[string]any{
		"seq":        seq,
		"byteLength": len(chunk.PCM16LE),
	})
	select {
	case s.outboundCh <- outboundMessage{pairHeader: header, pairBinary: chunk.PCM16LE}:
	case <-s.doneCh:
	default:
		s.markOverflow()
	}
}

// markOverflow 在出站队列被慢客户端撑满时触发：标记会话被踢，关闭 doneCh，
// readLoop 检测到 conn 关闭后会走正常 cleanup 流程释放上游资源。
func (s *sessionContext) markOverflow() {
	if !s.closed.CompareAndSwap(false, true) {
		return
	}
	s.overflow.Store(true)
	s.closeDone()
	_ = s.conn.Close()
}

func (s *sessionContext) sendPing(timeout time.Duration) bool {
	if s.closed.Load() {
		return false
	}
	deadline := time.Now().Add(timeout)
	if timeout <= 0 {
		deadline = time.Now().Add(10 * time.Second)
	}
	if err := s.conn.WriteControl(websocket.PingMessage, nil, deadline); err != nil {
		return false
	}
	return true
}

type reserveResult int

const (
	reserveOK reserveResult = iota
	reserveConflict
	reserveLimitExceeded
)

func (s *sessionContext) reserveTaskID(taskID string, maxTasks int) reserveResult {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	if _, exists := s.taskIDs[taskID]; exists {
		return reserveConflict
	}
	if maxTasks > 0 && len(s.taskIDs) >= maxTasks {
		return reserveLimitExceeded
	}
	s.taskIDs[taskID] = struct{}{}
	return reserveOK
}

func (s *sessionContext) releaseTaskID(taskID string) {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	delete(s.taskIDs, taskID)
}

func (s *sessionContext) setAsrTask(taskID string, task *asrTask) {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	s.asrTasks[taskID] = task
}

func (s *sessionContext) getAsrTask(taskID string) *asrTask {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	return s.asrTasks[taskID]
}

func (s *sessionContext) deleteAsrTask(taskID string) {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	delete(s.asrTasks, taskID)
}

func (s *sessionContext) listAsrTasks() []*asrTask {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	out := make([]*asrTask, 0, len(s.asrTasks))
	for _, task := range s.asrTasks {
		out = append(out, task)
	}
	return out
}

func (s *sessionContext) setTtsTask(taskID string, task *ttsTask) {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	s.ttsTasks[taskID] = task
}

func (s *sessionContext) getTtsTask(taskID string) *ttsTask {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	return s.ttsTasks[taskID]
}

func (s *sessionContext) deleteTtsTask(taskID string) {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	delete(s.ttsTasks, taskID)
}

func (s *sessionContext) listTtsTasks() []*ttsTask {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	out := make([]*ttsTask, 0, len(s.ttsTasks))
	for _, task := range s.ttsTasks {
		out = append(out, task)
	}
	return out
}

type queuedEvent struct {
	payload      string
	payloadBytes int
}

type asrTask struct {
	taskID                  string
	sampleRate              int
	language                string
	turnDetectionPayload    string
	pendingClientEvents     []queuedEvent
	pendingClientEventBytes int
	seenUpstreamEventTypes  map[string]struct{}
	upstream                asr.RealtimeUpstreamSession
	upstreamReady           bool
	stopped                 atomic.Bool
	mu                      sync.Mutex
}

type ttsTask struct {
	taskID        string
	mode          string
	inputMode     string
	text          string
	chatID        string
	agentKey      string
	plan          tts.SessionPlan
	stopped       atomic.Bool
	audioSequence atomic.Int64
	hasContent    atomic.Bool
	committed     atomic.Bool
	runnerCancel  context.CancelFunc
}

type upstreamListener struct {
	onMessage func(payload string)
	onClose   func(statusCode int, reason string)
	onError   func(err error)
}

func (l *upstreamListener) OnOpen() {}

func (l *upstreamListener) OnMessage(payload string) {
	if l.onMessage != nil {
		l.onMessage(payload)
	}
}

func (l *upstreamListener) OnClose(statusCode int, reason string) {
	if l.onClose != nil {
		l.onClose(statusCode, reason)
	}
}

func (l *upstreamListener) OnError(err error) {
	if l.onError != nil {
		l.onError(err)
	}
}

func eventBody(eventType, sessionID, taskID string, extras map[string]any) map[string]any {
	body := map[string]any{
		"type":      eventType,
		"sessionId": sessionID,
	}
	if strings.TrimSpace(taskID) != "" {
		body["taskId"] = taskID
	}
	for key, value := range extras {
		body[key] = value
	}
	return body
}

func buildAsrSessionUpdatePayload(defaults config.TurnDetectionProperties, raw json.RawMessage, sampleRate int, language string) string {
	turnDetection := map[string]any{
		"type":                defaults.Type,
		"threshold":           defaults.Threshold,
		"silence_duration_ms": defaults.SilenceDurationMs,
	}
	if defaults.PrefixPaddingMs > 0 {
		turnDetection["prefix_padding_ms"] = defaults.PrefixPaddingMs
	}
	if len(raw) > 0 {
		var input map[string]any
		if err := json.Unmarshal(raw, &input); err == nil {
			if value, ok := input["type"].(string); ok && strings.TrimSpace(value) != "" {
				turnDetection["type"] = value
			}
			if value, ok := input["threshold"].(float64); ok {
				turnDetection["threshold"] = value
			}
			if value, ok := input["silenceDurationMs"].(float64); ok {
				turnDetection["silence_duration_ms"] = int(value)
			}
			if value, ok := input["prefixPaddingMs"].(float64); ok {
				turnDetection["prefix_padding_ms"] = int(value)
			}
		}
	}

	payload := map[string]any{
		"type": "session.update",
		"session": map[string]any{
			"modalities":         []string{"text"},
			"input_audio_format": "pcm",
			"sample_rate":        sampleRate,
			"input_audio_transcription": map[string]any{
				"language": language,
			},
			"turn_detection": turnDetection,
		},
	}
	encoded, _ := json.Marshal(payload)
	return string(encoded)
}

func extractDeltaText(message map[string]any) string {
	eventType, _ := message["type"].(string)
	switch eventType {
	case "response.audio_transcript.delta":
		return firstNonBlank(anyString(message["delta"]), anyString(message["text"]))
	case "conversation.item.input_audio_transcription.text":
		return firstNonBlank(anyString(message["text"]), anyString(message["delta"]))
	default:
		return ""
	}
}

func extractFinalText(message map[string]any) string {
	eventType, _ := message["type"].(string)
	switch eventType {
	case "response.audio_transcript.done":
		return firstNonBlank(anyString(message["transcript"]), anyString(message["text"]), anyString(message["output_text"]))
	case "conversation.item.input_audio_transcription.completed":
		return firstNonBlank(anyString(message["transcript"]), anyString(message["text"]))
	case "response.done":
		direct := firstNonBlank(anyString(message["transcript"]), anyString(message["text"]), anyString(message["output_text"]))
		if direct != "" {
			return direct
		}
		if response, ok := message["response"].(map[string]any); ok {
			if output := extractOutputText(response["output"]); output != "" {
				return output
			}
		}
		return extractOutputText(message["output"])
	default:
		return ""
	}
}

func extractOutputText(value any) string {
	items, ok := value.([]any)
	if !ok {
		return ""
	}
	for _, item := range items {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		itemText := firstNonBlank(anyString(itemMap["transcript"]), anyString(itemMap["text"]), anyString(itemMap["output_text"]))
		if itemText != "" {
			return itemText
		}
		content, ok := itemMap["content"].([]any)
		if !ok {
			continue
		}
		for _, child := range content {
			childMap, ok := child.(map[string]any)
			if !ok {
				continue
			}
			childText := firstNonBlank(anyString(childMap["transcript"]), anyString(childMap["text"]), anyString(childMap["output_text"]))
			if childText != "" {
				return childText
			}
		}
	}
	return ""
}

func nestedString(message map[string]any, keys ...string) string {
	var current any = message
	for _, key := range keys {
		next, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = next[key]
	}
	return anyString(current)
}

func anyString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return ""
	}
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func defaultInt(value, fallback int) int {
	if value == 0 {
		return fallback
	}
	return value
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func compactJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var buffer bytes.Buffer
	if err := json.Compact(&buffer, raw); err == nil {
		return buffer.String()
	}
	return strings.TrimSpace(string(raw))
}

func compactVoiceDisplayName(voiceID, displayName string) string {
	if strings.EqualFold(strings.TrimSpace(voiceID), strings.TrimSpace(displayName)) {
		return ""
	}
	return displayName
}

// detailField 是日志字段构造的轻量包装，所有调用点都通过它拼接 slog.Attr，
// 让现有的代码路径不需要随 logger 实现切换而大改。空 string 值会返回零值
// slog.Attr，logTaskEvent 会过滤掉，避免输出 voice_display_name="" 这种噪音。
func detailField(key string, value any) slog.Attr {
	if s, ok := value.(string); ok && strings.TrimSpace(s) == "" {
		return slog.Attr{}
	}
	return slog.Any(key, value)
}

func filterEmptyAttrs(attrs []slog.Attr) []slog.Attr {
	out := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		if a.Key == "" {
			continue
		}
		out = append(out, a)
	}
	return out
}
