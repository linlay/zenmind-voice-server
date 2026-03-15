package asr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"zenmind-voice-server/internal/config"
)

type RealtimeUpstreamGateway interface {
	Connect(ctx context.Context, clientSessionID string, options ConnectOptions, listener UpstreamListener) (RealtimeUpstreamSession, error)
}

type ConnectOptions struct {
	Model string
}

type RealtimeUpstreamSession interface {
	IsOpen() bool
	SendText(payload string) error
	Close(statusCode int, reason string) error
}

type UpstreamListener interface {
	OnOpen()
	OnMessage(payload string)
	OnClose(statusCode int, reason string)
	OnError(err error)
}

type DashScopeRealtimeGateway struct {
	props  config.RealtimeProxyProperties
	dialer *websocket.Dialer
}

func NewDashScopeRealtimeGateway(app *config.App) *DashScopeRealtimeGateway {
	timeout := time.Duration(app.Asr.Realtime.ConnectTimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &DashScopeRealtimeGateway{
		props: app.Asr.Realtime,
		dialer: &websocket.Dialer{
			HandshakeTimeout: timeout,
		},
	}
}

func (g *DashScopeRealtimeGateway) Connect(ctx context.Context, _ string, options ConnectOptions, listener UpstreamListener) (RealtimeUpstreamSession, error) {
	apiKey := strings.TrimSpace(g.props.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("DASHSCOPE_API_KEY is missing")
	}

	timeout := time.Duration(g.props.ConnectTimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+apiKey)
	headers.Set("user-agent", "zenmind-voice-server")

	model := strings.TrimSpace(options.Model)
	if model == "" {
		model = g.props.Model
	}

	conn, _, err := g.dialer.DialContext(ctx, buildRealtimeURL(g.props.BaseURL, model), headers)
	if err != nil {
		return nil, err
	}

	session := &dashScopeRealtimeSession{
		conn:     conn,
		listener: listener,
	}
	listener.OnOpen()
	go session.readLoop()
	return session, nil
}

type dashScopeRealtimeSession struct {
	conn      *websocket.Conn
	listener  UpstreamListener
	closed    atomic.Bool
	closing   atomic.Bool
	closeOnce sync.Once
	writeMu   sync.Mutex
}

func (s *dashScopeRealtimeSession) IsOpen() bool {
	return !s.closed.Load()
}

func (s *dashScopeRealtimeSession) SendText(payload string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closed.Load() {
		return errors.New("upstream session already closed")
	}
	return s.conn.WriteMessage(websocket.TextMessage, []byte(payload))
}

func (s *dashScopeRealtimeSession) Close(statusCode int, reason string) error {
	s.closing.Store(true)
	var closeErr error
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
		closeErr = s.conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(statusCode, reason),
			time.Now().Add(time.Second),
		)
		_ = s.conn.Close()
	})
	return closeErr
}

func (s *dashScopeRealtimeSession) readLoop() {
	defer func() {
		s.closeOnce.Do(func() {
			s.closed.Store(true)
			_ = s.conn.Close()
		})
	}()

	for {
		messageType, payload, err := s.conn.ReadMessage()
		if err != nil {
			if s.closing.Load() || s.closed.Load() {
				return
			}
			if closeErr, ok := err.(*websocket.CloseError); ok {
				s.closed.Store(true)
				if !isExpectedCloseCode(closeErr.Code) {
					s.listener.OnError(closeErr)
				}
				s.listener.OnClose(closeErr.Code, closeErr.Text)
				return
			}
			s.listener.OnError(err)
			s.listener.OnClose(websocket.CloseInternalServerErr, err.Error())
			return
		}
		if messageType != websocket.TextMessage {
			continue
		}
		s.listener.OnMessage(string(payload))
	}
}

func isExpectedCloseCode(code int) bool {
	return code == websocket.CloseNormalClosure || code == websocket.CloseGoingAway
}

func buildRealtimeURL(baseURL, model string) string {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return baseURL + "?model=" + url.QueryEscape(model)
	}
	query := parsed.Query()
	query.Set("model", model)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
