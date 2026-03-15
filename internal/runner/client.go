package runner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"zenmind-voice-server/internal/config"
)

type Client interface {
	StreamEvents(ctx context.Context, message, chatID string) (<-chan Event, <-chan error)
}

type Event struct {
	Type   string
	Delta  string
	ChatID string
}

func (e Event) IsContentDelta() bool {
	return e.Type == "content.delta"
}

func (e Event) IsChatUpdated() bool {
	return e.Type == "chat.updated"
}

type HTTPClient struct {
	props      config.RunnerProperties
	httpClient *http.Client
}

func NewHTTPClient(app *config.App) *HTTPClient {
	timeout := time.Duration(app.Tts.Llm.Runner.RequestTimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return &HTTPClient{
		props: app.Tts.Llm.Runner,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

func (c *HTTPClient) StreamEvents(ctx context.Context, message, chatID string) (<-chan Event, <-chan error) {
	eventCh := make(chan Event, 16)
	errCh := make(chan error, 1)

	go func() {
		defer close(eventCh)
		defer close(errCh)

		if !c.props.IsConfigured() {
			errCh <- fmt.Errorf("Runner SSE is not configured")
			return
		}

		req, err := c.buildRequest(ctx, message, chatID)
		if err != nil {
			errCh <- err
			return
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			errCh <- fmt.Errorf("Runner SSE request failed: %w", err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(resp.Body)
			errCh <- fmt.Errorf("Runner request failed: url=%s, status=%d%s", req.URL.String(), resp.StatusCode, formatRunnerBody(body))
			return
		}

		if err := c.readSSE(resp.Body, eventCh); err != nil && ctx.Err() == nil {
			errCh <- err
		}
	}()

	return eventCh, errCh
}

func (c *HTTPClient) buildRequest(ctx context.Context, message, chatID string) (*http.Request, error) {
	payload, err := json.Marshal(c.BuildRequestPayload(message, chatID))
	if err != nil {
		return nil, err
	}

	baseURL := strings.TrimRight(strings.TrimSpace(c.props.BaseURL), "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/query", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	if token := strings.TrimSpace(c.props.AuthorizationToken); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req, nil
}

func (c *HTTPClient) BuildRequestPayload(message, chatID string) map[string]any {
	payload := map[string]any{
		"message":  message,
		"agentKey": c.props.AgentKey,
		"stream":   true,
	}
	if strings.TrimSpace(chatID) != "" {
		payload["chatId"] = chatID
	}
	return payload
}

func (c *HTTPClient) readSSE(body io.Reader, eventCh chan<- Event) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var dataLines []string
	var lastChatID string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			done, nextChatID, err := c.processEvent(strings.Join(dataLines, "\n"), lastChatID, eventCh)
			if err != nil {
				return err
			}
			if nextChatID != "" {
				lastChatID = nextChatID
			}
			dataLines = dataLines[:0]
			if done {
				return nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if len(dataLines) > 0 {
		_, _, err := c.processEvent(strings.Join(dataLines, "\n"), lastChatID, eventCh)
		return err
	}
	return nil
}

func (c *HTTPClient) processEvent(data, lastChatID string, eventCh chan<- Event) (bool, string, error) {
	data = strings.TrimSpace(data)
	if data == "" {
		return false, "", nil
	}
	if data == "[DONE]" {
		return true, "", nil
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return false, "", err
	}

	event := c.ToRunnerEvent(payload, lastChatID)
	if event.Type == "" {
		return false, "", nil
	}
	eventCh <- event
	if event.IsChatUpdated() {
		return false, event.ChatID, nil
	}
	return false, "", nil
}

func (c *HTTPClient) ToRunnerEvent(payload map[string]any, lastChatID string) Event {
	eventType, _ := payload["type"].(string)
	switch eventType {
	case "content.delta":
		delta, _ := payload["delta"].(string)
		if strings.TrimSpace(delta) == "" {
			return Event{}
		}
		return Event{Type: "content.delta", Delta: delta}
	case "request.query", "chat.start":
		chatID, _ := payload["chatId"].(string)
		if strings.TrimSpace(chatID) == "" || chatID == lastChatID {
			return Event{}
		}
		return Event{Type: "chat.updated", ChatID: chatID}
	default:
		return Event{}
	}
}

func formatRunnerBody(body []byte) string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return ""
	}
	return ", body=" + text
}
