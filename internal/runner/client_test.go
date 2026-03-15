package runner

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"zenmind-voice-server/internal/config"
)

func TestBuildRequestPayload(t *testing.T) {
	app := &config.App{}
	app.Tts.Llm.Runner.AgentKey = "demo"
	client := NewHTTPClient(app)

	payload := client.BuildRequestPayload("hello", "chat-1")
	if payload["message"] != "hello" {
		t.Fatalf("unexpected message: %#v", payload["message"])
	}
	if payload["agentKey"] != "demo" {
		t.Fatalf("unexpected agentKey: %#v", payload["agentKey"])
	}
	if payload["chatId"] != "chat-1" {
		t.Fatalf("unexpected chatId: %#v", payload["chatId"])
	}
}

func TestToRunnerEvent(t *testing.T) {
	app := &config.App{}
	client := NewHTTPClient(app)

	event := client.ToRunnerEvent(map[string]any{
		"type":  "content.delta",
		"delta": "hello",
	}, "")
	if !event.IsContentDelta() || event.Delta != "hello" {
		t.Fatalf("unexpected content event: %+v", event)
	}

	chatEvent := client.ToRunnerEvent(map[string]any{
		"type":   "chat.start",
		"chatId": "chat-2",
	}, "")
	if !chatEvent.IsChatUpdated() || chatEvent.ChatID != "chat-2" {
		t.Fatalf("unexpected chat event: %+v", chatEvent)
	}
}

func TestStreamEvents(t *testing.T) {
	requestPathCh := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPathCh <- r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"chat.start\",\"chatId\":\"chat-1\"}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content.delta\",\"delta\":\"hello\"}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	app := &config.App{}
	app.Tts.Llm.Runner.BaseURL = server.URL
	app.Tts.Llm.Runner.AgentKey = "demo"
	app.Tts.Llm.Runner.RequestTimeoutMs = 2000
	client := NewHTTPClient(app)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	eventCh, errCh := client.StreamEvents(ctx, "hello", "")
	var events []Event
	for event := range eventCh {
		events = append(events, event)
	}
	for err := range errCh {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	select {
	case path := <-requestPathCh:
		if path != "/api/query" {
			t.Fatalf("unexpected request path: %s", path)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for request path")
	}
	if !events[0].IsChatUpdated() || events[0].ChatID != "chat-1" {
		t.Fatalf("unexpected first event: %+v", events[0])
	}
	if !events[1].IsContentDelta() || events[1].Delta != "hello" {
		t.Fatalf("unexpected second event: %+v", events[1])
	}
}

func TestBuildRequestUsesQueryEndpoint(t *testing.T) {
	app := &config.App{}
	app.Tts.Llm.Runner.BaseURL = "http://runner.local"
	app.Tts.Llm.Runner.AgentKey = "demo"
	client := NewHTTPClient(app)

	req, err := client.buildRequest(context.Background(), "hello", "chat-1")
	if err != nil {
		t.Fatalf("build request: %v", err)
	}

	if req.Method != http.MethodPost {
		t.Fatalf("unexpected method: %s", req.Method)
	}
	if req.URL.Path != "/api/query" {
		t.Fatalf("unexpected path: %s", req.URL.Path)
	}
	if req.Header.Get("Accept") != "text/event-stream" {
		t.Fatalf("unexpected Accept header: %q", req.Header.Get("Accept"))
	}
}

func TestStreamEventsErrorIncludesRequestURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))
	defer server.Close()

	app := &config.App{}
	app.Tts.Llm.Runner.BaseURL = server.URL
	app.Tts.Llm.Runner.AgentKey = "demo"
	client := NewHTTPClient(app)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, errCh := client.StreamEvents(ctx, "hello", "")
	for err := range errCh {
		if err == nil {
			continue
		}
		parsed, parseErr := url.Parse(server.URL)
		if parseErr != nil {
			t.Fatalf("parse server url: %v", parseErr)
		}
		expectedURL := parsed.String() + "/api/query"
		if got := err.Error(); got != fmt.Sprintf("Runner request failed: url=%s, status=404, body=404 page not found", expectedURL) &&
			got != fmt.Sprintf("Runner request failed: url=%s, status=404, body=missing", expectedURL) {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	t.Fatal("expected runner error")
}
