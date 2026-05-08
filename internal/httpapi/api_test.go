package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"zenmind-voice-server/internal/config"
	"zenmind-voice-server/internal/health"
	"zenmind-voice-server/internal/tts"
)

func TestCapabilities(t *testing.T) {
	app := &config.App{}
	*app = *configTestApp()
	api := New(app, tts.NewVoiceCatalog(app))
	mux := http.NewServeMux()
	api.Register(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/voice/capabilities", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["websocketPath"] != "/api/voice/ws" {
		t.Fatalf("unexpected websocket path: %#v", payload["websocketPath"])
	}
	asrPayload := payload["asr"].(map[string]any)
	defaults := asrPayload["defaults"].(map[string]any)
	clientGate := defaults["clientGate"].(map[string]any)
	if clientGate["enabled"] != true {
		t.Fatalf("expected client gate enabled, got %#v", clientGate["enabled"])
	}
	if clientGate["rmsThreshold"] != 0.008 {
		t.Fatalf("unexpected client gate threshold: %#v", clientGate["rmsThreshold"])
	}
	if clientGate["openHoldMs"] != float64(120) {
		t.Fatalf("unexpected client gate openHoldMs: %#v", clientGate["openHoldMs"])
	}
	if clientGate["closeHoldMs"] != float64(480) {
		t.Fatalf("unexpected client gate closeHoldMs: %#v", clientGate["closeHoldMs"])
	}
	if clientGate["preRollMs"] != float64(240) {
		t.Fatalf("unexpected client gate preRollMs: %#v", clientGate["preRollMs"])
	}
	ttsPayload := payload["tts"].(map[string]any)
	if ttsPayload["streamInput"] != true {
		t.Fatalf("expected streamInput=true")
	}
	deprecatedModes := ttsPayload["deprecatedModes"].([]any)
	if len(deprecatedModes) != 1 || deprecatedModes[0] != "llm" {
		t.Fatalf("unexpected deprecatedModes: %#v", deprecatedModes)
	}
	if ttsPayload["defaultMode"] != "local" {
		t.Fatalf("unexpected defaultMode: %#v", ttsPayload["defaultMode"])
	}
	if ttsPayload["runnerConfigured"] != true {
		t.Fatalf("expected runnerConfigured=true")
	}
	audioFormat := ttsPayload["audioFormat"].(map[string]any)
	if audioFormat["responseFormat"] != "pcm" {
		t.Fatalf("unexpected responseFormat: %#v", audioFormat["responseFormat"])
	}
	if audioFormat["sampleRate"] != float64(24000) {
		t.Fatalf("unexpected sampleRate: %#v", audioFormat["sampleRate"])
	}
}

func TestVoices(t *testing.T) {
	app := &config.App{}
	*app = *configTestApp()
	api := New(app, tts.NewVoiceCatalog(app))
	mux := http.NewServeMux()
	api.Register(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/voice/tts/voices", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload struct {
		DefaultVoice string `json:"defaultVoice"`
		Voices       []struct {
			ID      string `json:"id"`
			Default bool   `json:"default"`
		} `json:"voices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.DefaultVoice != "Cherry" {
		t.Fatalf("unexpected default voice: %s", payload.DefaultVoice)
	}
	if len(payload.Voices) != 2 {
		t.Fatalf("expected 2 voices, got %d", len(payload.Voices))
	}
	if payload.Voices[0].ID != "Cherry" || !payload.Voices[0].Default {
		t.Fatalf("unexpected first voice: %+v", payload.Voices[0])
	}
}

func TestLivenessAlwaysUp(t *testing.T) {
	app := configTestApp()
	api := New(app, tts.NewVoiceCatalog(app))
	mux := http.NewServeMux()
	api.Register(mux)

	for _, path := range []string{"/actuator/health", "/actuator/health/liveness"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s expected 200, got %d", path, rec.Code)
		}
	}
}

func TestReadinessReturns503WhenUpstreamFails(t *testing.T) {
	app := configTestApp()
	asrProbe := health.New()
	ttsProbe := health.New()
	api := NewWithProbes(app, tts.NewVoiceCatalog(app), asrProbe, ttsProbe, nil)
	mux := http.NewServeMux()
	api.Register(mux)

	for i := 0; i < 5; i++ {
		asrProbe.ObserveFailure()
	}
	req := httptest.NewRequest(http.MethodGet, "/actuator/health/readiness", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 after upstream failures, got %d body=%s", rec.Code, rec.Body.String())
	}

	for i := 0; i < 10; i++ {
		asrProbe.ObserveSuccess()
	}
	req = httptest.NewRequest(http.MethodGet, "/actuator/health/readiness", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 after recovery, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestReadinessReturns503WhenDraining(t *testing.T) {
	app := configTestApp()
	api := NewWithProbes(app, tts.NewVoiceCatalog(app), health.New(), health.New(), DrainGateFunc(func() bool { return true }))
	mux := http.NewServeMux()
	api.Register(mux)

	req := httptest.NewRequest(http.MethodGet, "/actuator/health/readiness", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when draining, got %d", rec.Code)
	}
}

func configTestApp() *config.App {
	app := &config.App{
		ServerPort: 11953,
	}
	app.Asr.Realtime.APIKey = "sk-asr"
	app.Asr.ClientGate.Enabled = true
	app.Asr.ClientGate.RMSThreshold = 0.008
	app.Asr.ClientGate.OpenHoldMs = 120
	app.Asr.ClientGate.CloseHoldMs = 480
	app.Asr.ClientGate.PreRollMs = 240
	app.Tts.DefaultMode = "local"
	app.Tts.Local.APIKey = "sk-tts"
	app.Tts.Local.ResponseFormat = "pcm"
	app.Tts.Local.SpeechRate = 1.2
	app.Tts.Llm.Runner.BaseURL = "http://localhost:8081"
	app.Tts.Llm.Runner.AgentKey = "demo"
	app.Tts.Voices.DefaultVoice = "Cherry"
	app.Tts.Voices.Options = []config.VoiceOption{
		{ID: "Cherry", DisplayName: "Cherry", Provider: "dashscope"},
		{ID: "Serena", DisplayName: "Serena", Provider: "dashscope"},
	}
	return app
}
