package tts

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"zenmind-voice-server/internal/config"
	"zenmind-voice-server/internal/core"
)

type DashScopeRealtimeClient struct {
	props  config.LocalTtsProperties
	dialer *websocket.Dialer
}

func NewDashScopeRealtimeClient(app *config.App) *DashScopeRealtimeClient {
	return &DashScopeRealtimeClient{
		props: app.Tts.Local,
		dialer: &websocket.Dialer{
			HandshakeTimeout: 10 * time.Second,
		},
	}
}

func (c *DashScopeRealtimeClient) OpenSession(options core.TtsRequestOptions) (TtsStreamSession, error) {
	endpoint := strings.TrimSpace(c.props.Endpoint)
	if endpoint == "" {
		return nil, fmt.Errorf("missing configuration: dashscope tts endpoint")
	}
	apiKey := strings.TrimSpace(c.props.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("missing configuration: dashscope tts api-key")
	}
	model := firstNonBlank(strings.TrimSpace(options.Model), strings.TrimSpace(c.props.Model))
	if model == "" {
		return nil, fmt.Errorf("missing configuration: dashscope tts model")
	}
	voice := strings.TrimSpace(options.Voice)
	if voice == "" {
		return nil, fmt.Errorf("missing configuration: dashscope tts voice")
	}

	rawResponseFormat := firstNonBlank(options.ResponseFormat, c.props.ResponseFormat)
	responseFormat := NormalizeResponseFormat(rawResponseFormat)
	sampleRate := ParseSampleRate(rawResponseFormat)
	speechRate := c.props.SpeechRate
	if options.SpeechRate != nil {
		speechRate = clampSpeechRate(*options.SpeechRate)
	}

	session := &dashScopeTtsStreamSession{
		dialer:         c.dialer,
		endpoint:       endpoint,
		apiKey:         apiKey,
		model:          model,
		voice:          voice,
		mode:           firstNonBlank(options.Mode, c.props.Mode),
		responseFormat: responseFormat,
		sampleRate:     sampleRate,
		speechRate:     speechRate,
		instructions:   strings.TrimSpace(options.Instructions),
		audioCh:        make(chan core.AudioChunk, 32),
		doneCh:         make(chan struct{}),
		errCh:          make(chan error, 1),
	}
	go session.run()
	return session, nil
}

type dashScopeTtsStreamSession struct {
	dialer         *websocket.Dialer
	endpoint       string
	apiKey         string
	model          string
	voice          string
	mode           string
	responseFormat string
	sampleRate     int
	speechRate     float64
	instructions   string

	audioCh chan core.AudioChunk
	doneCh  chan struct{}
	errCh   chan error

	mu         sync.Mutex
	conn       *websocket.Conn
	pending    []string
	finished   bool
	terminated bool
	ready      bool
	seq        uint64
}

func (s *dashScopeTtsStreamSession) AudioChan() <-chan core.AudioChunk {
	return s.audioCh
}

func (s *dashScopeTtsStreamSession) DoneChan() <-chan struct{} {
	return s.doneCh
}

func (s *dashScopeTtsStreamSession) ErrChan() <-chan error {
	return s.errCh
}

func (s *dashScopeTtsStreamSession) SampleRate() int {
	return s.sampleRate
}

func (s *dashScopeTtsStreamSession) Channels() int {
	return 1
}

func (s *dashScopeTtsStreamSession) AppendText(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminated {
		return
	}
	if s.ready && s.conn != nil {
		_ = s.conn.WriteJSON(map[string]any{
			"event_id": s.nextEventID(),
			"type":     "input_text_buffer.append",
			"text":     text,
		})
		return
	}
	s.pending = append(s.pending, text)
}

func (s *dashScopeTtsStreamSession) Finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminated || s.finished {
		return
	}
	s.finished = true
	if s.ready && s.conn != nil {
		_ = s.conn.WriteJSON(map[string]any{
			"event_id": s.nextEventID(),
			"type":     "session.finish",
		})
	}
}

func (s *dashScopeTtsStreamSession) Cancel() {
	s.mu.Lock()
	if s.terminated {
		s.mu.Unlock()
		return
	}
	s.terminated = true
	conn := s.conn
	s.conn = nil
	s.mu.Unlock()

	if conn != nil {
		_ = conn.Close()
	}
	s.complete()
}

func (s *dashScopeTtsStreamSession) run() {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+s.apiKey)
	headers.Set("user-agent", "zenmind-voice-server")

	conn, _, err := s.dialer.Dial(buildDashScopeRealtimeURL(s.endpoint, s.model), headers)
	if err != nil {
		s.fail(fmt.Errorf("realtime TTS request failed: %w", err))
		return
	}

	s.mu.Lock()
	if s.terminated {
		s.mu.Unlock()
		_ = conn.Close()
		return
	}
	s.conn = conn
	s.ready = true
	s.mu.Unlock()

	if err := conn.WriteJSON(map[string]any{
		"event_id": s.nextEventID(),
		"type":     "session.update",
		"session":  s.sessionConfig(),
	}); err != nil {
		_ = conn.Close()
		s.fail(fmt.Errorf("realtime TTS session update failed: %w", err))
		return
	}
	if err := s.flushPending(); err != nil {
		_ = conn.Close()
		s.fail(err)
		return
	}

	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			s.mu.Lock()
			terminated := s.terminated
			s.mu.Unlock()
			if terminated {
				return
			}
			s.fail(fmt.Errorf("realtime TTS request failed: %w", err))
			return
		}

		var event map[string]any
		if err := json.Unmarshal(payload, &event); err != nil {
			continue
		}
		eventType, _ := event["type"].(string)
		switch eventType {
		case "response.audio.delta":
			delta, _ := event["delta"].(string)
			if strings.TrimSpace(delta) == "" {
				continue
			}
			pcm, err := base64.StdEncoding.DecodeString(delta)
			if err != nil {
				s.fail(fmt.Errorf("decode TTS audio delta: %w", err))
				return
			}
			chunk, err := core.NewAudioChunk(pcm, s.sampleRate, 1)
			if err != nil {
				s.fail(err)
				return
			}
			select {
			case s.audioCh <- chunk:
			case <-time.After(5 * time.Second):
				s.fail(fmt.Errorf("tts audio channel blocked"))
				return
			}
		case "error":
			code, message := parseRealtimeTTSError(event)
			if strings.TrimSpace(message) == "" {
				log.Printf("tts upstream error payload=%q", string(payload))
				message = "Realtime TTS upstream returned error"
			} else if strings.TrimSpace(code) != "" {
				log.Printf("tts upstream error code=%q message=%q", code, message)
			}
			s.fail(errors.New(message))
			return
		case "session.finished":
			_ = conn.Close()
			s.complete()
			return
		}
	}
}

func (s *dashScopeTtsStreamSession) flushPending() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return nil
	}
	for _, text := range s.pending {
		if err := s.conn.WriteJSON(map[string]any{
			"event_id": s.nextEventID(),
			"type":     "input_text_buffer.append",
			"text":     text,
		}); err != nil {
			return fmt.Errorf("append TTS text failed: %w", err)
		}
	}
	s.pending = nil
	if s.finished {
		if err := s.conn.WriteJSON(map[string]any{
			"event_id": s.nextEventID(),
			"type":     "session.finish",
		}); err != nil {
			return fmt.Errorf("finish TTS session failed: %w", err)
		}
	}
	return nil
}

func (s *dashScopeTtsStreamSession) sessionConfig() map[string]any {
	session := map[string]any{
		"voice":           s.voice,
		"mode":            s.mode,
		"response_format": s.responseFormat,
		"sample_rate":     s.sampleRate,
		"speech_rate":     s.speechRate,
		"enable_tn":       true,
	}
	if s.instructions != "" {
		session["instructions"] = s.instructions
		session["optimize_instructions"] = true
	}
	return session
}

func (s *dashScopeTtsStreamSession) nextEventID() string {
	value := atomic.AddUint64(&s.seq, 1)
	return fmt.Sprintf("event_%d", value)
}

func (s *dashScopeTtsStreamSession) fail(err error) {
	s.mu.Lock()
	if s.terminated {
		s.mu.Unlock()
		return
	}
	s.terminated = true
	s.mu.Unlock()

	select {
	case s.errCh <- err:
	default:
	}
	s.complete()
}

func (s *dashScopeTtsStreamSession) complete() {
	s.mu.Lock()
	if s.conn != nil {
		_ = s.conn.Close()
		s.conn = nil
	}
	if !s.terminated {
		s.terminated = true
	}
	s.mu.Unlock()

	select {
	case <-s.doneCh:
		return
	default:
		close(s.doneCh)
		close(s.audioCh)
	}
}

func buildDashScopeRealtimeURL(baseURL, model string) string {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return baseURL + "?model=" + url.QueryEscape(model)
	}
	query := parsed.Query()
	query.Set("model", model)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func clampSpeechRate(rate float64) float64 {
	if rate < 0.5 {
		return 0.5
	}
	if rate > 2.0 {
		return 2.0
	}
	return rate
}

func parseRealtimeTTSError(event map[string]any) (string, string) {
	code := firstNonBlank(anyString(event["code"]), nestedString(event, "error", "code"))
	message := firstNonBlank(anyString(event["message"]), nestedString(event, "error", "message"))
	return code, message
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
