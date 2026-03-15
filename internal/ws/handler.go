package ws

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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

type Handler struct {
	app        *config.App
	upstream   asr.RealtimeUpstreamGateway
	ttsService *tts.SynthesisService
	runner     runner.Client
	upgrader   websocket.Upgrader
}

func NewHandler(app *config.App, upstream asr.RealtimeUpstreamGateway, ttsService *tts.SynthesisService, runnerClient runner.Client) *Handler {
	return &Handler{
		app:        app,
		upstream:   upstream,
		ttsService: ttsService,
		runner:     runnerClient,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(_ *http.Request) bool { return true },
		},
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	session := newSessionContext(conn)
	session.sendJSON(map[string]any{
		"type":            "connection.ready",
		"sessionId":       session.sessionID,
		"protocolVersion": "v2",
		"capabilities": map[string]any{
			"asr":      true,
			"tts":      true,
			"ttsModes": []string{"local", "llm"},
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

type clientEvent struct {
	Type          string          `json:"type"`
	TaskID        string          `json:"taskId"`
	SampleRate    int             `json:"sampleRate"`
	Language      string          `json:"language"`
	Audio         string          `json:"audio"`
	TurnDetection json.RawMessage `json:"turnDetection"`
	Mode          string          `json:"mode"`
	Text          string          `json:"text"`
	Voice         string          `json:"voice"`
	ChatID        string          `json:"chatId"`
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
	if !session.reserveTaskID(taskID) {
		h.sendError(session, taskID, "task_conflict", "taskId is already active")
		return
	}
	if !h.app.Asr.Realtime.HasAPIKey() {
		session.releaseTaskID(taskID)
		h.sendError(session, taskID, "config_missing", "DASHSCOPE_API_KEY is missing")
		return
	}

	task := &asrTask{
		taskID:                 taskID,
		sampleRate:             defaultInt(event.SampleRate, 16000),
		language:               defaultString(event.Language, "zh"),
		pendingClientEvents:    make([]queuedEvent, 0, 8),
		seenUpstreamEventTypes: make(map[string]struct{}),
	}
	task.turnDetectionPayload = buildAsrSessionUpdatePayload(event.TurnDetection, task.sampleRate, task.language)
	session.setAsrTask(taskID, task)

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
	audio, ok := h.validateAudioAppend(session, taskID, event, originalPayload)
	if !ok {
		return
	}
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
	h.finishAsrTask(session, task, "client_stop", true, true)
}

func (h *Handler) handleTtsStart(session *sessionContext, event clientEvent) {
	taskID := strings.TrimSpace(event.TaskID)
	if taskID == "" {
		h.sendError(session, "", "bad_request", "taskId must not be blank")
		return
	}
	if !session.reserveTaskID(taskID) {
		h.sendError(session, taskID, "task_conflict", "taskId is already active")
		return
	}

	mode := defaultString(event.Mode, h.app.Tts.DefaultMode)
	text := strings.TrimSpace(event.Text)
	if mode != "local" && mode != "llm" {
		session.releaseTaskID(taskID)
		h.sendError(session, taskID, "bad_request", "Unsupported tts mode: "+mode)
		return
	}
	if text == "" {
		session.releaseTaskID(taskID)
		h.sendError(session, taskID, "bad_request", "tts.start requires non-empty text")
		return
	}
	if !h.ttsService.IsLocalConfigured() {
		session.releaseTaskID(taskID)
		h.sendError(session, taskID, "config_missing", "DASHSCOPE_TTS_API_KEY is missing")
		return
	}
	if mode == "llm" && !h.app.Tts.Llm.Runner.IsConfigured() {
		session.releaseTaskID(taskID)
		h.sendError(session, taskID, "config_missing", "Runner SSE is not configured")
		return
	}

	plan, err := h.ttsService.OpenSession(event.Voice, event.SpeechRate)
	if err != nil {
		session.releaseTaskID(taskID)
		h.sendLoggedError(session, taskID, "tts", "tts_failed", err.Error(), err, "")
		return
	}

	task := &ttsTask{
		taskID: taskID,
		mode:   mode,
		text:   text,
		chatID: strings.TrimSpace(event.ChatID),
		plan:   plan,
	}
	session.setTtsTask(taskID, task)
	h.startTtsTask(session, task)
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
	session.sendJSON(eventBody("tts.done", session.sessionID, taskID, map[string]any{"reason": "client_stop"}))
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
			h.finishAsrTask(session, task, reason, false, true)
		},
		onError: func(err error) {
			h.sendLoggedError(session, task.taskID, "asr", "upstream_error", "Upstream realtime service error", err, "")
			h.finishAsrTask(session, task, "upstream_error", false, true)
		},
	})
	if err != nil {
		h.sendLoggedError(session, task.taskID, "asr", "upstream_connect_failed", "Failed to connect upstream realtime service", err, "")
		h.finishAsrTask(session, task, "connect_failed", false, true)
		return
	}

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
	h.flushPendingClientEvents(session, task)
}

func (h *Handler) startTtsTask(session *sessionContext, task *ttsTask) {
	session.sendJSON(eventBody("task.started", session.sessionID, task.taskID, map[string]any{
		"taskType": "tts",
		"mode":     task.mode,
	}))
	session.sendJSON(eventBody("tts.audio.format", session.sessionID, task.taskID, map[string]any{
		"sampleRate":       task.plan.SampleRate,
		"channels":         task.plan.Channels,
		"voice":            task.plan.VoiceID,
		"voiceDisplayName": task.plan.VoiceDisplayName,
	}))

	go h.streamTtsAudio(session, task)
	if task.mode == "local" {
		task.plan.Session.AppendText(task.text)
		task.plan.Session.Finish()
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	task.runnerCancel = cancel
	eventCh, errCh := h.runner.StreamEvents(ctx, task.text, task.chatID)
	go func() {
		for {
			select {
			case event, ok := <-eventCh:
				if !ok {
					eventCh = nil
					if !task.hasContent.Load() {
						session.sendJSON(eventBody("tts.done", session.sessionID, task.taskID, map[string]any{"reason": "no_content"}))
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
					continue
				}
				if !event.IsContentDelta() || strings.TrimSpace(event.Delta) == "" {
					continue
				}
				task.hasContent.Store(true)
				session.sendJSON(eventBody("tts.text.delta", session.sessionID, task.taskID, map[string]any{
					"text": event.Delta,
				}))
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
		h.sendLoggedError(session, task.taskID, "asr", code, message, nil, payload)
		h.finishAsrTask(session, task, code, false, true)
		return
	}

	if deltaText := extractDeltaText(event); strings.TrimSpace(deltaText) != "" {
		session.sendJSON(eventBody("asr.text.delta", session.sessionID, task.taskID, map[string]any{
			"text":         deltaText,
			"upstreamType": eventType,
		}))
	}
	if finalText := extractFinalText(event); strings.TrimSpace(finalText) != "" {
		session.sendJSON(eventBody("asr.text.final", session.sessionID, task.taskID, map[string]any{
			"text":         finalText,
			"upstreamType": eventType,
		}))
	}
	if eventType == "input_audio_buffer.speech_started" {
		session.sendJSON(eventBody("asr.speech.started", session.sessionID, task.taskID, map[string]any{
			"upstreamType": eventType,
		}))
	}
	if eventType == "session.finished" {
		h.finishAsrTask(session, task, "upstream_finished", false, true)
	}
}

func (h *Handler) validateAudioAppend(session *sessionContext, taskID string, event clientEvent, originalPayload []byte) (string, bool) {
	realtime := h.app.Asr.Realtime
	if len(originalPayload) > realtime.MaxClientEventBytes {
		h.sendError(session, taskID, "event_too_large", "Client event exceeds maximum size")
		return "", false
	}
	audio := strings.TrimSpace(event.Audio)
	if audio == "" {
		h.sendError(session, taskID, "bad_request", "audio.append requires non-empty string field 'audio'")
		return "", false
	}
	if len(audio) > realtime.MaxAppendAudioChars {
		h.sendError(session, taskID, "audio_too_large", "Audio payload exceeds maximum size")
		return "", false
	}
	if _, err := base64.StdEncoding.DecodeString(audio); err != nil {
		h.sendError(session, taskID, "bad_request", "audio must be valid base64 pcm16le")
		return "", false
	}
	return audio, true
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
}

func (h *Handler) cleanup(session *sessionContext, notify bool) {
	if !session.closed.CompareAndSwap(false, true) {
		return
	}
	for _, task := range session.listAsrTasks() {
		h.finishAsrTask(session, task, "connection_closed", true, notify)
	}
	for _, task := range session.listTtsTasks() {
		h.finishTtsTask(session, task, "connection_closed", true, notify)
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
	logParts := []string{
		fmt.Sprintf("session_id=%q", session.sessionID),
		fmt.Sprintf("task_id=%q", taskID),
		fmt.Sprintf("task_type=%q", taskType),
		fmt.Sprintf("code=%q", code),
		fmt.Sprintf("message=%q", message),
	}
	if cause != nil {
		logParts = append(logParts, fmt.Sprintf("cause=%q", cause.Error()))
	}
	if strings.TrimSpace(upstreamPayload) != "" {
		logParts = append(logParts, fmt.Sprintf("upstream_payload=%q", upstreamPayload))
	}
	log.Printf("voice backend error %s", strings.Join(logParts, " "))
	h.sendError(session, taskID, code, message)
}

type sessionContext struct {
	conn      *websocket.Conn
	sessionID string
	writeMu   sync.Mutex
	taskMu    sync.Mutex
	taskIDs   map[string]struct{}
	asrTasks  map[string]*asrTask
	ttsTasks  map[string]*ttsTask
	closed    atomic.Bool
}

func newSessionContext(conn *websocket.Conn) *sessionContext {
	return &sessionContext{
		conn:      conn,
		sessionID: fmt.Sprintf("ws-session-%d", time.Now().UnixNano()),
		taskIDs:   make(map[string]struct{}),
		asrTasks:  make(map[string]*asrTask),
		ttsTasks:  make(map[string]*ttsTask),
	}
}

func (s *sessionContext) sendJSON(payload map[string]any) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closed.Load() {
		return
	}
	_ = s.conn.WriteJSON(payload)
}

func (s *sessionContext) sendTtsChunkPair(taskID string, seq int, chunk core.AudioChunk) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closed.Load() {
		return
	}
	_ = s.conn.WriteJSON(eventBody("tts.audio.chunk", s.sessionID, taskID, map[string]any{
		"seq":        seq,
		"byteLength": len(chunk.PCM16LE),
	}))
	_ = s.conn.WriteMessage(websocket.BinaryMessage, chunk.PCM16LE)
}

func (s *sessionContext) reserveTaskID(taskID string) bool {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	if _, exists := s.taskIDs[taskID]; exists {
		return false
	}
	s.taskIDs[taskID] = struct{}{}
	return true
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
	text          string
	chatID        string
	plan          tts.SessionPlan
	stopped       atomic.Bool
	audioSequence atomic.Int64
	hasContent    atomic.Bool
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

func buildAsrSessionUpdatePayload(raw json.RawMessage, sampleRate int, language string) string {
	turnDetection := map[string]any{
		"type":                "server_vad",
		"threshold":           0.0,
		"silence_duration_ms": 400,
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

func classifyTransportError(err error) int {
	var closeErr *websocket.CloseError
	if errors.As(err, &closeErr) {
		return closeErr.Code
	}
	return websocket.CloseInternalServerErr
}
