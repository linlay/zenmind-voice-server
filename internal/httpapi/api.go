package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"zenmind-voice-server/internal/config"
	"zenmind-voice-server/internal/health"
	"zenmind-voice-server/internal/tts"
)

// minHealthRatio 是 readiness 判定的成功率阈值；近期窗口内成功率低于此值视为
// 上游不健康，readiness 返回 503。
const minHealthRatio = 0.5

// DrainGate 抽象 ws.Handler 的 draining 状态以便 readiness 判断；实际由 ws 包实现。
type DrainGate interface {
	IsDraining() bool
}

type drainGateFunc func() bool

func (f drainGateFunc) IsDraining() bool { return f() }

// DrainGateFunc 让调用方用闭包简单包装 draining 状态。
func DrainGateFunc(f func() bool) DrainGate { return drainGateFunc(f) }

type API struct {
	app          *config.App
	voiceCatalog *tts.VoiceCatalog
	asrProbe     *health.ConnectProbe
	ttsProbe     *health.ConnectProbe
	drainGate    DrainGate
}

func New(app *config.App, voiceCatalog *tts.VoiceCatalog) *API {
	return &API{app: app, voiceCatalog: voiceCatalog}
}

// NewWithProbes 接收上游探针和 draining gate，是生产路径上的构造函数；
// 老的 New 保留给只关心配置的测试。
func NewWithProbes(app *config.App, voiceCatalog *tts.VoiceCatalog, asrProbe, ttsProbe *health.ConnectProbe, drainGate DrainGate) *API {
	return &API{
		app:          app,
		voiceCatalog: voiceCatalog,
		asrProbe:     asrProbe,
		ttsProbe:     ttsProbe,
		drainGate:    drainGate,
	}
}

func (a *API) Register(mux *http.ServeMux) {
	mux.HandleFunc("/api/voice/capabilities", a.capabilities)
	mux.HandleFunc("/api/voice/tts/voices", a.voices)
	mux.HandleFunc("/actuator/health", a.liveness)
	mux.HandleFunc("/actuator/health/liveness", a.liveness)
	mux.HandleFunc("/actuator/health/readiness", a.readiness)
}

func (a *API) capabilities(w http.ResponseWriter, _ *http.Request) {
	clientGate := a.app.Asr.ClientGate.Normalized()
	turnDetection := a.app.Asr.TurnDetection.Normalized()
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
					"type":              turnDetection.Type,
					"threshold":         turnDetection.Threshold,
					"silenceDurationMs": turnDetection.SilenceDurationMs,
					"prefixPaddingMs":   turnDetection.PrefixPaddingMs,
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

func (a *API) liveness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "UP",
	})
}

func (a *API) readiness(w http.ResponseWriter, _ *http.Request) {
	checks := map[string]any{}
	overallOK := true

	if a.drainGate != nil && a.drainGate.IsDraining() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "OUT_OF_SERVICE",
			"reason": "draining",
		})
		return
	}

	asrOK := evaluateUpstream(a.app.Asr.Realtime.HasAPIKey(), a.asrProbe)
	checks["asr"] = upstreamCheckBody(asrOK, a.asrProbe)
	if !asrOK.healthy {
		overallOK = false
	}

	ttsOK := evaluateUpstream(a.app.Tts.Local.HasAPIKey(), a.ttsProbe)
	checks["tts"] = upstreamCheckBody(ttsOK, a.ttsProbe)
	if !ttsOK.healthy {
		overallOK = false
	}

	status := http.StatusOK
	statusText := "UP"
	if !overallOK {
		status = http.StatusServiceUnavailable
		statusText = "DOWN"
	}
	writeJSON(w, status, map[string]any{
		"status": statusText,
		"checks": checks,
	})
}

type upstreamEval struct {
	healthy    bool
	configured bool
	reason     string
}

func evaluateUpstream(configured bool, probe *health.ConnectProbe) upstreamEval {
	if !configured {
		return upstreamEval{healthy: false, configured: false, reason: "missing_api_key"}
	}
	if probe == nil {
		return upstreamEval{healthy: true, configured: true}
	}
	samples, ratio := probe.Snapshot()
	if samples == 0 {
		return upstreamEval{healthy: true, configured: true, reason: "no_recent_samples"}
	}
	if ratio < minHealthRatio {
		return upstreamEval{healthy: false, configured: true, reason: "low_success_ratio"}
	}
	return upstreamEval{healthy: true, configured: true}
}

func upstreamCheckBody(eval upstreamEval, probe *health.ConnectProbe) map[string]any {
	body := map[string]any{
		"configured": eval.configured,
		"healthy":    eval.healthy,
	}
	if eval.reason != "" {
		body["reason"] = eval.reason
	}
	if probe != nil {
		samples, ratio := probe.Snapshot()
		body["samples"] = samples
		body["successRatio"] = ratio
	}
	return body
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
