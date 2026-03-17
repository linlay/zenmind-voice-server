package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"zenmind-voice-server/internal/config"
	"zenmind-voice-server/internal/tts"
)

type API struct {
	app          *config.App
	voiceCatalog *tts.VoiceCatalog
}

func New(app *config.App, voiceCatalog *tts.VoiceCatalog) *API {
	return &API{app: app, voiceCatalog: voiceCatalog}
}

func (a *API) Register(mux *http.ServeMux) {
	mux.HandleFunc("/api/voice/capabilities", a.capabilities)
	mux.HandleFunc("/api/voice/tts/voices", a.voices)
	mux.HandleFunc("/actuator/health", a.health)
}

func (a *API) capabilities(w http.ResponseWriter, _ *http.Request) {
	clientGate := a.app.Asr.ClientGate.Normalized()
	writeJSON(w, http.StatusOK, map[string]any{
		"websocketPath": "/api/voice/ws",
		"asr": map[string]any{
			"configured": a.app.Asr.Realtime.HasAPIKey(),
			"defaults": map[string]any{
				"sampleRate": 16000,
				"language":   "zh",
				"clientGate": map[string]any{
					"enabled":      clientGate.Enabled,
					"rmsThreshold": clientGate.RMSThreshold,
					"openHoldMs":   clientGate.OpenHoldMs,
					"closeHoldMs":  clientGate.CloseHoldMs,
					"preRollMs":    clientGate.PreRollMs,
				},
				"turnDetection": map[string]any{
					"type":              "server_vad",
					"threshold":         0,
					"silenceDurationMs": 400,
				},
			},
		},
		"tts": map[string]any{
			"modes":             []string{"local", "llm"},
			"deprecatedModes":   []string{"llm"},
			"streamInput":       true,
			"defaultMode":       a.app.Tts.DefaultMode,
			"speechRateDefault": a.app.Tts.Local.SpeechRate,
			"audioFormat": map[string]any{
				"sampleRate":     tts.ParseSampleRate(a.app.Tts.Local.ResponseFormat),
				"channels":       1,
				"responseFormat": tts.NormalizeResponseFormat(a.app.Tts.Local.ResponseFormat),
			},
			"runnerConfigured": a.app.Tts.Llm.Runner.IsConfigured(),
			"voicesEndpoint":   "/api/voice/tts/voices",
		},
	})
}

func (a *API) voices(w http.ResponseWriter, _ *http.Request) {
	voices := make([]map[string]any, 0, len(a.voiceCatalog.ListVoices()))
	defaultVoice := a.voiceCatalog.DefaultVoiceID()
	for _, voice := range a.voiceCatalog.ListVoices() {
		displayName := strings.TrimSpace(voice.DisplayName)
		if displayName == "" {
			displayName = voice.ID
		}
		voices = append(voices, map[string]any{
			"id":          voice.ID,
			"displayName": displayName,
			"provider":    voice.Provider,
			"default":     strings.EqualFold(voice.ID, defaultVoice),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"defaultVoice": defaultVoice,
		"voices":       voices,
	})
}

func (a *API) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "UP",
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
