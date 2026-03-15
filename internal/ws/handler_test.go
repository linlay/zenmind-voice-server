package ws

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"zenmind-voice-server/internal/asr"
	"zenmind-voice-server/internal/config"
	"zenmind-voice-server/internal/core"
	"zenmind-voice-server/internal/runner"
	"zenmind-voice-server/internal/tts"
)

func TestConnectionReady(t *testing.T) {
	app, gateway, runnerClient, ttsClient := testDependencies()
	handler := NewHandler(app, gateway, tts.NewSynthesisService(app, tts.NewVoiceCatalog(app), ttsClient), runnerClient)
	server := httptest.NewServer(handler)
	defer server.Close()

	conn := dialWS(t, server.URL)
	defer conn.Close()

	message := readJSONMessage(t, conn)
	if message["type"] != "connection.ready" {
		t.Fatalf("unexpected type: %#v", message["type"])
	}
	if message["protocolVersion"] != "v2" {
		t.Fatalf("unexpected protocolVersion: %#v", message["protocolVersion"])
	}
}

func TestHandlerMountedAtVoiceWebSocketPath(t *testing.T) {
	app, gateway, runnerClient, ttsClient := testDependencies()
	handler := NewHandler(app, gateway, tts.NewSynthesisService(app, tts.NewVoiceCatalog(app), ttsClient), runnerClient)

	mux := http.NewServeMux()
	mux.Handle("/api/voice/ws", handler)

	server := httptest.NewServer(mux)
	defer server.Close()

	conn := dialWS(t, server.URL+"/api/voice/ws")
	defer conn.Close()

	message := readJSONMessage(t, conn)
	if message["type"] != "connection.ready" {
		t.Fatalf("unexpected type: %#v", message["type"])
	}
}

func TestQueueAsrEventsBeforeUpstreamReady(t *testing.T) {
	app, gateway, runnerClient, ttsClient := testDependencies()
	gateway.connectGate = make(chan struct{})
	handler := NewHandler(app, gateway, tts.NewSynthesisService(app, tts.NewVoiceCatalog(app), ttsClient), runnerClient)
	server := httptest.NewServer(handler)
	defer server.Close()

	conn := dialWS(t, server.URL)
	defer conn.Close()
	_ = readJSONMessage(t, conn)

	writeJSON(t, conn, map[string]any{
		"type":       "asr.start",
		"taskId":     "asr-main",
		"sampleRate": 16000,
		"language":   "zh",
	})
	writeJSON(t, conn, map[string]any{
		"type":   "asr.audio.append",
		"taskId": "asr-main",
		"audio":  "AQID",
	})

	time.Sleep(100 * time.Millisecond)
	if len(gateway.upstream.sentPayloads()) != 0 {
		t.Fatalf("expected no payloads before upstream ready")
	}

	close(gateway.connectGate)
	waitFor(t, 2*time.Second, func() bool {
		return len(gateway.upstream.sentPayloads()) == 2
	})

	payloads := gateway.upstream.sentPayloads()
	if !strings.Contains(payloads[0], `"type":"session.update"`) {
		t.Fatalf("unexpected first payload: %s", payloads[0])
	}
	if !strings.Contains(payloads[1], `"input_audio_buffer.append"`) {
		t.Fatalf("unexpected second payload: %s", payloads[1])
	}
}

func TestAsrQueueOverflow(t *testing.T) {
	app, gateway, runnerClient, ttsClient := testDependencies()
	app.Asr.Realtime.MaxPendingClientEvents = 1
	gateway.connectGate = make(chan struct{})
	handler := NewHandler(app, gateway, tts.NewSynthesisService(app, tts.NewVoiceCatalog(app), ttsClient), runnerClient)
	server := httptest.NewServer(handler)
	defer server.Close()

	conn := dialWS(t, server.URL)
	defer conn.Close()
	_ = readJSONMessage(t, conn)

	writeJSON(t, conn, map[string]any{"type": "asr.start", "taskId": "asr-main"})
	writeJSON(t, conn, map[string]any{"type": "asr.audio.append", "taskId": "asr-main", "audio": "AQID"})
	writeJSON(t, conn, map[string]any{"type": "asr.audio.append", "taskId": "asr-main", "audio": "AQID"})

	first := readJSONMessage(t, conn)
	second := readJSONMessage(t, conn)
	if first["type"] != "error" && second["type"] != "error" {
		t.Fatalf("expected an error event")
	}
}

func TestAsrStopDoesNotEmitUpstreamError(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade: %v", err)
		}
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer upstreamServer.Close()

	app, _, runnerClient, ttsClient := testDependencies()
	app.Asr.Realtime.BaseURL = httpToWS(t, upstreamServer.URL)
	handler := NewHandler(app, asr.NewDashScopeRealtimeGateway(app), tts.NewSynthesisService(app, tts.NewVoiceCatalog(app), ttsClient), runnerClient)
	server := httptest.NewServer(handler)
	defer server.Close()

	conn := dialWS(t, server.URL)
	defer conn.Close()
	_ = readJSONMessage(t, conn)

	writeJSON(t, conn, map[string]any{"type": "asr.start", "taskId": "asr-stop"})

	started := readJSONMessage(t, conn)
	if started["type"] != "task.started" {
		t.Fatalf("expected task.started, got %#v", started)
	}

	writeJSON(t, conn, map[string]any{"type": "asr.stop", "taskId": "asr-stop"})

	for {
		message := readJSONMessage(t, conn)
		if message["type"] == "error" {
			t.Fatalf("unexpected error event: %#v", message)
		}
		if message["type"] == "task.stopped" {
			break
		}
	}

	conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	_, payload, err := conn.ReadMessage()
	if err == nil {
		var message map[string]any
		if jsonErr := json.Unmarshal(payload, &message); jsonErr == nil && message["type"] == "error" {
			t.Fatalf("unexpected trailing error event: %#v", message)
		}
	}
}

func TestLocalTtsStreamsAudioAndBinary(t *testing.T) {
	app, gateway, runnerClient, ttsClient := testDependencies()
	handler := NewHandler(app, gateway, tts.NewSynthesisService(app, tts.NewVoiceCatalog(app), ttsClient), runnerClient)
	server := httptest.NewServer(handler)
	defer server.Close()

	conn := dialWS(t, server.URL)
	defer conn.Close()
	_ = readJSONMessage(t, conn)

	writeJSON(t, conn, map[string]any{
		"type":       "tts.start",
		"taskId":     "tts-main",
		"mode":       "local",
		"text":       "hello",
		"voice":      "Cherry",
		"speechRate": 1.5,
	})

	var textEvents []map[string]any
	var binaryFrames int
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		messageType, payload, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if messageType == websocket.BinaryMessage {
			binaryFrames++
			continue
		}
		var message map[string]any
		if err := json.Unmarshal(payload, &message); err != nil {
			t.Fatalf("decode message: %v", err)
		}
		textEvents = append(textEvents, message)
		if message["type"] == "task.stopped" {
			break
		}
	}

	if binaryFrames != 1 {
		t.Fatalf("expected 1 binary frame, got %d", binaryFrames)
	}
	if !containsEvent(textEvents, "task.started") || !containsEvent(textEvents, "tts.audio.format") || !containsEvent(textEvents, "tts.audio.chunk") || !containsEvent(textEvents, "tts.done") || !containsEvent(textEvents, "task.stopped") {
		t.Fatalf("unexpected text events: %#v", textEvents)
	}
	if ttsClient.lastSpeechRate() != 1.5 {
		t.Fatalf("unexpected speech rate: %v", ttsClient.lastSpeechRate())
	}
}

func TestLlmTtsStreamsTextDelta(t *testing.T) {
	app, gateway, runnerClient, ttsClient := testDependencies()
	runnerClient.events = []runner.Event{
		{Type: "chat.updated", ChatID: "chat-1"},
		{Type: "content.delta", Delta: "你好"},
		{Type: "content.delta", Delta: "，世界"},
	}
	handler := NewHandler(app, gateway, tts.NewSynthesisService(app, tts.NewVoiceCatalog(app), ttsClient), runnerClient)
	server := httptest.NewServer(handler)
	defer server.Close()

	conn := dialWS(t, server.URL)
	defer conn.Close()
	_ = readJSONMessage(t, conn)

	writeJSON(t, conn, map[string]any{
		"type":   "tts.start",
		"taskId": "tts-main",
		"mode":   "llm",
		"text":   "summarize",
		"voice":  "Cherry",
	})

	var textEvents []map[string]any
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		messageType, payload, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if messageType == websocket.BinaryMessage {
			continue
		}
		var message map[string]any
		if err := json.Unmarshal(payload, &message); err != nil {
			t.Fatalf("decode message: %v", err)
		}
		textEvents = append(textEvents, message)
		if message["type"] == "task.stopped" {
			break
		}
	}

	if !containsEvent(textEvents, "tts.text.delta") || !containsEvent(textEvents, "tts.chat.updated") || !containsEvent(textEvents, "tts.done") {
		t.Fatalf("unexpected llm events: %#v", textEvents)
	}
}

func testDependencies() (*config.App, *fakeGateway, *fakeRunnerClient, *fakeRealtimeTtsClient) {
	app := &config.App{}
	app.ServerPort = 11953
	app.Asr.Realtime.APIKey = "sk-asr"
	app.Asr.Realtime.MaxClientEventBytes = 1048576
	app.Asr.Realtime.MaxAppendAudioChars = 2097152
	app.Asr.Realtime.MaxPendingClientEvents = 128
	app.Asr.Realtime.MaxPendingClientBytes = 1048576
	app.Tts.DefaultMode = "local"
	app.Tts.Local.APIKey = "sk-tts"
	app.Tts.Local.Model = "qwen3-tts-instruct-flash-realtime"
	app.Tts.Local.Mode = "server_commit"
	app.Tts.Local.ResponseFormat = "pcm"
	app.Tts.Local.SpeechRate = 1.2
	app.Tts.Llm.Runner.BaseURL = "http://runner"
	app.Tts.Llm.Runner.AgentKey = "demo"
	app.Tts.Voices.DefaultVoice = "Cherry"
	app.Tts.Voices.Options = []config.VoiceOption{
		{ID: "Cherry", DisplayName: "Cherry", Provider: "dashscope"},
	}
	return app, &fakeGateway{upstream: &fakeUpstreamSession{open: true}}, &fakeRunnerClient{}, &fakeRealtimeTtsClient{}
}

type fakeGateway struct {
	connectGate chan struct{}
	upstream    *fakeUpstreamSession
	listener    asr.UpstreamListener
}

func (g *fakeGateway) Connect(_ context.Context, _ string, _ asr.ConnectOptions, listener asr.UpstreamListener) (asr.RealtimeUpstreamSession, error) {
	g.listener = listener
	if g.connectGate != nil {
		<-g.connectGate
	}
	listener.OnOpen()
	return g.upstream, nil
}

type fakeUpstreamSession struct {
	mu       sync.Mutex
	open     bool
	payloads []string
}

func (s *fakeUpstreamSession) IsOpen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.open
}

func (s *fakeUpstreamSession) SendText(payload string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.payloads = append(s.payloads, payload)
	return nil
}

func (s *fakeUpstreamSession) Close(_ int, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.open = false
	return nil
}

func (s *fakeUpstreamSession) sentPayloads() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.payloads...)
}

type fakeRealtimeTtsClient struct {
	mu       sync.Mutex
	lastRate float64
}

func (c *fakeRealtimeTtsClient) OpenSession(options core.TtsRequestOptions) (tts.TtsStreamSession, error) {
	rate := 0.0
	if options.SpeechRate != nil {
		rate = *options.SpeechRate
	}
	c.mu.Lock()
	c.lastRate = rate
	c.mu.Unlock()
	session := &fakeTtsSession{
		audioCh: make(chan core.AudioChunk, 1),
		doneCh:  make(chan struct{}),
		errCh:   make(chan error, 1),
	}
	return session, nil
}

func (c *fakeRealtimeTtsClient) lastSpeechRate() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastRate
}

type fakeTtsSession struct {
	audioCh   chan core.AudioChunk
	doneCh    chan struct{}
	errCh     chan error
	closeOnce sync.Once
}

func (s *fakeTtsSession) AudioChan() <-chan core.AudioChunk { return s.audioCh }
func (s *fakeTtsSession) DoneChan() <-chan struct{}         { return s.doneCh }
func (s *fakeTtsSession) ErrChan() <-chan error             { return s.errCh }
func (s *fakeTtsSession) SampleRate() int                   { return 24000 }
func (s *fakeTtsSession) Channels() int                     { return 1 }
func (s *fakeTtsSession) AppendText(string)                 {}
func (s *fakeTtsSession) Finish() {
	s.closeOnce.Do(func() {
		chunk, _ := core.NewAudioChunk([]byte{1, 2, 3, 4}, 24000, 1)
		s.audioCh <- chunk
		close(s.audioCh)
		go func() {
			time.Sleep(20 * time.Millisecond)
			close(s.doneCh)
		}()
	})
}
func (s *fakeTtsSession) Cancel() {
	s.closeOnce.Do(func() {
		close(s.audioCh)
		close(s.doneCh)
	})
}

type fakeRunnerClient struct {
	events []runner.Event
	err    error
}

func (c *fakeRunnerClient) StreamEvents(_ context.Context, _, _ string) (<-chan runner.Event, <-chan error) {
	eventCh := make(chan runner.Event, len(c.events))
	errCh := make(chan error, 1)
	go func() {
		for _, event := range c.events {
			eventCh <- event
		}
		close(eventCh)
		if c.err != nil {
			errCh <- c.err
		}
		close(errCh)
	}()
	return eventCh, errCh
}

func dialWS(t *testing.T, raw string) *websocket.Conn {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	parsed.Scheme = "ws"
	conn, _, err := websocket.DefaultDialer.Dial(parsed.String(), nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	return conn
}

func httpToWS(t *testing.T, raw string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	if strings.HasPrefix(parsed.Scheme, "http") {
		parsed.Scheme = "ws"
	}
	return parsed.String()
}

func writeJSON(t *testing.T, conn *websocket.Conn, payload map[string]any) {
	t.Helper()
	if err := conn.WriteJSON(payload); err != nil {
		t.Fatalf("write json: %v", err)
	}
}

func readJSONMessage(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read message: %v", err)
	}
	var message map[string]any
	if err := json.Unmarshal(payload, &message); err != nil {
		t.Fatalf("decode message: %v", err)
	}
	return message
}

func containsEvent(events []map[string]any, eventType string) bool {
	for _, event := range events {
		if event["type"] == eventType {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, timeout time.Duration, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for condition")
}
