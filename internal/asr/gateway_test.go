package asr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"zenmind-voice-server/internal/config"
)

func TestConnectFailsWhenAPIKeyMissing(t *testing.T) {
	app := &config.App{}
	gateway := NewDashScopeRealtimeGateway(app)

	_, err := gateway.Connect(context.Background(), "client-1", ConnectOptions{}, noopListener{})
	if err == nil || err.Error() != "DASHSCOPE_API_KEY is missing" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConnectAndSendText(t *testing.T) {
	received := make(chan string, 4)
	server := newWebSocketTestServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		for {
			_, payload, err := conn.ReadMessage()
			if err != nil {
				return
			}
			received <- string(payload)
		}
	})
	defer server.Close()

	app := &config.App{}
	app.Asr.Realtime.APIKey = "sk-demo"
	app.Asr.Realtime.BaseURL = httpToWS(t, server.URL)
	gateway := NewDashScopeRealtimeGateway(app)

	listener := &capturingListener{}
	session, err := gateway.Connect(context.Background(), "client-2", ConnectOptions{Model: "model-a"}, listener)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if listener.openCount != 1 {
		t.Fatalf("expected openCount=1, got %d", listener.openCount)
	}
	if err := session.SendText(`{"type":"session.update"}`); err != nil {
		t.Fatalf("send text: %v", err)
	}

	select {
	case payload := <-received:
		if payload != `{"type":"session.update"}` {
			t.Fatalf("unexpected payload: %s", payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for upstream payload")
	}
}

func TestCloseDoesNotTriggerOnError(t *testing.T) {
	server := newWebSocketTestServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	defer server.Close()

	app := &config.App{}
	app.Asr.Realtime.APIKey = "sk-demo"
	app.Asr.Realtime.BaseURL = httpToWS(t, server.URL)
	gateway := NewDashScopeRealtimeGateway(app)

	listener := &capturingListener{
		errorCh: make(chan error, 1),
		closeCh: make(chan closeEvent, 1),
	}
	session, err := gateway.Connect(context.Background(), "client-close", ConnectOptions{}, listener)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	if err := session.Close(websocket.CloseNormalClosure, "client stop"); err != nil {
		t.Fatalf("close: %v", err)
	}

	select {
	case err := <-listener.errorCh:
		t.Fatalf("expected no error callback, got %v", err)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestUnexpectedRemoteCloseTriggersErrorAndClose(t *testing.T) {
	server := newWebSocketTestServer(t, func(conn *websocket.Conn) {
		_ = conn.UnderlyingConn().Close()
	})
	defer server.Close()

	app := &config.App{}
	app.Asr.Realtime.APIKey = "sk-demo"
	app.Asr.Realtime.BaseURL = httpToWS(t, server.URL)
	gateway := NewDashScopeRealtimeGateway(app)

	listener := &capturingListener{
		errorCh: make(chan error, 1),
		closeCh: make(chan closeEvent, 1),
	}
	_, err := gateway.Connect(context.Background(), "client-abrupt", ConnectOptions{}, listener)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	select {
	case err := <-listener.errorCh:
		if err == nil {
			t.Fatal("expected non-nil error callback")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for error callback")
	}

	select {
	case evt := <-listener.closeCh:
		if evt.code == 0 {
			t.Fatal("expected close callback with status code")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for close callback")
	}
}

type capturingListener struct {
	mu        sync.Mutex
	openCount int
	errorCh   chan error
	closeCh   chan closeEvent
}

func (l *capturingListener) OnOpen() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.openCount++
}

func (l *capturingListener) OnMessage(string) {}

func (l *capturingListener) OnClose(code int, reason string) {
	if l.closeCh == nil {
		return
	}
	select {
	case l.closeCh <- closeEvent{code: code, reason: reason}:
	default:
	}
}

func (l *capturingListener) OnError(err error) {
	if l.errorCh == nil {
		return
	}
	select {
	case l.errorCh <- err:
	default:
	}
}

type closeEvent struct {
	code   int
	reason string
}

type noopListener struct{}

func (noopListener) OnOpen()             {}
func (noopListener) OnMessage(string)    {}
func (noopListener) OnClose(int, string) {}
func (noopListener) OnError(error)       {}

func newWebSocketTestServer(t *testing.T, fn func(conn *websocket.Conn)) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade: %v", err)
		}
		fn(conn)
	}))
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
