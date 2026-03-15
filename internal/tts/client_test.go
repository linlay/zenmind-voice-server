package tts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"zenmind-voice-server/internal/config"
	"zenmind-voice-server/internal/core"
)

func TestRealtimeTTSSendsPCMResponseFormat(t *testing.T) {
	sessionUpdate := make(chan map[string]any, 1)
	server := newTTSWebSocketTestServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		readSessionUpdate(t, conn, sessionUpdate)
	})
	defer server.Close()

	client := NewDashScopeRealtimeClient(testTTSApp(httpToWS(t, server.URL), "pcm"))
	session, err := client.OpenSession(core.TtsRequestOptions{Voice: "Cherry"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	defer session.Cancel()

	update := awaitSessionUpdate(t, sessionUpdate)
	sessionMap := update["session"].(map[string]any)
	if sessionMap["response_format"] != "pcm" {
		t.Fatalf("unexpected response_format: %#v", sessionMap["response_format"])
	}
}

func TestRealtimeTTSLegacyPCMFormatMapsToPCM(t *testing.T) {
	sessionUpdate := make(chan map[string]any, 1)
	server := newTTSWebSocketTestServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		readSessionUpdate(t, conn, sessionUpdate)
	})
	defer server.Close()

	client := NewDashScopeRealtimeClient(testTTSApp(httpToWS(t, server.URL), "PCM_24000HZ_MONO_16BIT"))
	session, err := client.OpenSession(core.TtsRequestOptions{Voice: "Cherry"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	defer session.Cancel()

	update := awaitSessionUpdate(t, sessionUpdate)
	sessionMap := update["session"].(map[string]any)
	if sessionMap["response_format"] != "pcm" {
		t.Fatalf("expected legacy format to map to pcm, got %#v", sessionMap["response_format"])
	}
}

func TestRealtimeTTSErrorUsesNestedMessage(t *testing.T) {
	server := newTTSWebSocketTestServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		if _, _, err := conn.ReadMessage(); err != nil {
			t.Fatalf("read session update: %v", err)
		}
		if err := conn.WriteJSON(map[string]any{
			"type": "error",
			"error": map[string]any{
				"code":    "tts_quota_exceeded",
				"message": "quota exceeded",
			},
		}); err != nil {
			t.Fatalf("write error message: %v", err)
		}
	})
	defer server.Close()

	client := NewDashScopeRealtimeClient(testTTSApp(httpToWS(t, server.URL), "pcm"))
	session, err := client.OpenSession(core.TtsRequestOptions{Voice: "Cherry"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}

	select {
	case err := <-session.ErrChan():
		if err == nil || err.Error() != "quota exceeded" {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for TTS error")
	}
}

func TestRealtimeTTSCancelDoesNotReportError(t *testing.T) {
	ready := make(chan struct{}, 1)
	server := newTTSWebSocketTestServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		ready <- struct{}{}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	defer server.Close()

	client := NewDashScopeRealtimeClient(testTTSApp(httpToWS(t, server.URL), "pcm"))
	session, err := client.OpenSession(core.TtsRequestOptions{Voice: "Cherry"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}

	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for TTS session setup")
	}

	session.Cancel()

	select {
	case err := <-session.ErrChan():
		t.Fatalf("expected no error after cancel, got %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	select {
	case <-session.DoneChan():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for session done")
	}
}

func TestRealtimeTTSSessionFinishedDoesNotReportError(t *testing.T) {
	finished := make(chan struct{}, 1)
	server := newTTSWebSocketTestServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		for {
			_, payload, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if strings.Contains(string(payload), `"type":"session.finish"`) {
				if err := conn.WriteJSON(map[string]any{"type": "session.finished"}); err != nil {
					t.Fatalf("write session.finished: %v", err)
				}
				finished <- struct{}{}
				return
			}
		}
	})
	defer server.Close()

	client := NewDashScopeRealtimeClient(testTTSApp(httpToWS(t, server.URL), "pcm"))
	session, err := client.OpenSession(core.TtsRequestOptions{Voice: "Cherry"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}

	session.Finish()

	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for session.finish")
	}

	select {
	case err := <-session.ErrChan():
		t.Fatalf("expected no error on normal finish, got %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	select {
	case <-session.DoneChan():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for session done")
	}
}

func testTTSApp(endpoint, responseFormat string) *config.App {
	app := &config.App{}
	app.Tts.Local.Endpoint = endpoint
	app.Tts.Local.APIKey = "sk-demo"
	app.Tts.Local.Model = "qwen3-tts-instruct-flash-realtime"
	app.Tts.Local.Mode = "server_commit"
	app.Tts.Local.ResponseFormat = responseFormat
	return app
}

func readSessionUpdate(t *testing.T, conn *websocket.Conn, out chan<- map[string]any) {
	t.Helper()
	_, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read session update: %v", err)
	}

	var event map[string]any
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatalf("decode session update: %v", err)
	}

	select {
	case out <- event:
	default:
	}
}

func awaitSessionUpdate(t *testing.T, updates <-chan map[string]any) map[string]any {
	t.Helper()
	select {
	case update := <-updates:
		return update
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for session update")
		return nil
	}
}

func newTTSWebSocketTestServer(t *testing.T, fn func(conn *websocket.Conn)) *httptest.Server {
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
