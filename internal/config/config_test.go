package config

import (
	"strings"
	"testing"
)

func TestApplyEnvLoadsRequiredVoiceConfig(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("APP_VOICE_TTS_LOCAL_SPEECH_RATE", "1.5")
	t.Setenv("APP_VOICE_ASR_WS_DETAILED_LOG_ENABLED", "true")
	t.Setenv("APP_VOICE_TTS_WS_DETAILED_LOG_ENABLED", "true")
	t.Setenv("APP_VOICE_ASR_CLIENT_GATE_ENABLED", "false")
	t.Setenv("APP_VOICE_ASR_CLIENT_GATE_RMS_THRESHOLD", "0.012")
	t.Setenv("APP_VOICE_ASR_CLIENT_GATE_OPEN_HOLD_MS", "150")
	t.Setenv("APP_VOICE_ASR_CLIENT_GATE_CLOSE_HOLD_MS", "600")
	t.Setenv("APP_VOICE_ASR_CLIENT_GATE_PRE_ROLL_MS", "180")
	t.Setenv("APP_VOICE_ASR_TURN_DETECTION_THRESHOLD", "0.6")
	t.Setenv("APP_VOICE_ASR_TURN_DETECTION_SILENCE_DURATION_MS", "900")
	t.Setenv("APP_VOICE_ASR_TURN_DETECTION_PREFIX_PADDING_MS", "360")

	cfg := defaults()
	if err := applyEnv(cfg); err != nil {
		t.Fatalf("applyEnv: %v", err)
	}
	if err := validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}

	if cfg.Asr.Realtime.Model != "asr-model-a" {
		t.Fatalf("unexpected ASR model: %q", cfg.Asr.Realtime.Model)
	}
	if cfg.Tts.Local.Model != "tts-model-a" {
		t.Fatalf("unexpected TTS model: %q", cfg.Tts.Local.Model)
	}
	if cfg.Tts.Voices.DefaultVoice != "voice-a" {
		t.Fatalf("unexpected default voice: %q", cfg.Tts.Voices.DefaultVoice)
	}
	if len(cfg.Tts.Voices.Options) != 2 {
		t.Fatalf("expected 2 voices, got %d", len(cfg.Tts.Voices.Options))
	}
	if cfg.Tts.Voices.Options[0].ID != "voice-a" {
		t.Fatalf("unexpected first voice: %+v", cfg.Tts.Voices.Options[0])
	}
	if cfg.Tts.Local.SpeechRate != 1.5 {
		t.Fatalf("unexpected speech rate: %v", cfg.Tts.Local.SpeechRate)
	}
	if !cfg.Asr.WebSocketDetailedLogEnabled {
		t.Fatal("expected ASR websocket detailed log to be enabled")
	}
	if !cfg.Tts.WebSocketDetailedLogEnabled {
		t.Fatal("expected TTS websocket detailed log to be enabled")
	}
	if cfg.Asr.ClientGate.Enabled {
		t.Fatal("expected client gate to be disabled by env override")
	}
	if cfg.Asr.ClientGate.RMSThreshold != 0.012 {
		t.Fatalf("unexpected client gate threshold: %v", cfg.Asr.ClientGate.RMSThreshold)
	}
	if cfg.Asr.ClientGate.OpenHoldMs != 150 {
		t.Fatalf("unexpected client gate open hold: %d", cfg.Asr.ClientGate.OpenHoldMs)
	}
	if cfg.Asr.ClientGate.CloseHoldMs != 600 {
		t.Fatalf("unexpected client gate close hold: %d", cfg.Asr.ClientGate.CloseHoldMs)
	}
	if cfg.Asr.ClientGate.PreRollMs != 180 {
		t.Fatalf("unexpected client gate pre-roll: %d", cfg.Asr.ClientGate.PreRollMs)
	}
	if cfg.Asr.TurnDetection.Threshold != 0.6 {
		t.Fatalf("unexpected turn detection threshold: %v", cfg.Asr.TurnDetection.Threshold)
	}
	if cfg.Asr.TurnDetection.SilenceDurationMs != 900 {
		t.Fatalf("unexpected turn detection silence duration: %d", cfg.Asr.TurnDetection.SilenceDurationMs)
	}
	if cfg.Asr.TurnDetection.PrefixPaddingMs != 360 {
		t.Fatalf("unexpected turn detection prefix padding: %d", cfg.Asr.TurnDetection.PrefixPaddingMs)
	}
}

func TestApplyEnvLeavesDetailedLogsDisabledByDefault(t *testing.T) {
	setRequiredEnv(t)

	cfg := defaults()
	if err := applyEnv(cfg); err != nil {
		t.Fatalf("applyEnv: %v", err)
	}

	if cfg.Asr.WebSocketDetailedLogEnabled {
		t.Fatal("expected ASR websocket detailed log to default to false")
	}
	if cfg.Tts.WebSocketDetailedLogEnabled {
		t.Fatal("expected TTS websocket detailed log to default to false")
	}
	if !cfg.Asr.ClientGate.Enabled {
		t.Fatal("expected ASR client gate to default to enabled")
	}
	if cfg.Asr.ClientGate.RMSThreshold != 0.012 {
		t.Fatalf("unexpected default client gate threshold: %v", cfg.Asr.ClientGate.RMSThreshold)
	}
	if cfg.Asr.ClientGate.OpenHoldMs != 200 {
		t.Fatalf("unexpected default client gate open hold: %d", cfg.Asr.ClientGate.OpenHoldMs)
	}
	if cfg.Asr.ClientGate.CloseHoldMs != 700 {
		t.Fatalf("unexpected default client gate close hold: %d", cfg.Asr.ClientGate.CloseHoldMs)
	}
	if cfg.Asr.ClientGate.PreRollMs != 240 {
		t.Fatalf("unexpected default client gate pre-roll: %d", cfg.Asr.ClientGate.PreRollMs)
	}
	if cfg.Asr.TurnDetection.Threshold != 0.5 {
		t.Fatalf("unexpected default turn detection threshold: %v", cfg.Asr.TurnDetection.Threshold)
	}
	if cfg.Asr.TurnDetection.SilenceDurationMs != 700 {
		t.Fatalf("unexpected default turn detection silence duration: %d", cfg.Asr.TurnDetection.SilenceDurationMs)
	}
	if cfg.Asr.TurnDetection.PrefixPaddingMs != 300 {
		t.Fatalf("unexpected default turn detection prefix padding: %d", cfg.Asr.TurnDetection.PrefixPaddingMs)
	}
}

func TestApplyEnvRejectsInvalidVoiceCatalogJSON(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("APP_VOICE_TTS_VOICES_JSON", "{not-json}")

	cfg := defaults()
	err := applyEnv(cfg)
	if err == nil || !strings.Contains(err.Error(), "APP_VOICE_TTS_VOICES_JSON") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRejectsMissingRequiredConfiguration(t *testing.T) {
	t.Setenv("APP_VOICE_ASR_REALTIME_BASE_URL", "")
	t.Setenv("APP_VOICE_ASR_REALTIME_MODEL", "")
	t.Setenv("APP_VOICE_TTS_LOCAL_ENDPOINT", "")
	t.Setenv("APP_VOICE_TTS_LOCAL_MODEL", "")
	t.Setenv("APP_VOICE_TTS_DEFAULT_VOICE", "")
	t.Setenv("APP_VOICE_TTS_VOICES_JSON", "")

	cfg := defaults()
	if err := applyEnv(cfg); err != nil {
		t.Fatalf("applyEnv: %v", err)
	}
	err := validate(cfg)
	if err == nil {
		t.Fatal("expected validation error")
	}
	for _, envName := range []string{
		"APP_VOICE_ASR_REALTIME_BASE_URL",
		"APP_VOICE_ASR_REALTIME_MODEL",
		"APP_VOICE_TTS_LOCAL_ENDPOINT",
		"APP_VOICE_TTS_LOCAL_MODEL",
		"APP_VOICE_TTS_DEFAULT_VOICE",
		"APP_VOICE_TTS_VOICES_JSON",
	} {
		if !strings.Contains(err.Error(), envName) {
			t.Fatalf("expected %s in error: %v", envName, err)
		}
	}
}

func TestValidateRejectsDefaultVoiceOutsideCatalog(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("APP_VOICE_TTS_DEFAULT_VOICE", "voice-missing")

	cfg := defaults()
	if err := applyEnv(cfg); err != nil {
		t.Fatalf("applyEnv: %v", err)
	}
	err := validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "APP_VOICE_TTS_DEFAULT_VOICE") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("APP_VOICE_ASR_REALTIME_BASE_URL", "wss://asr.example.com/realtime")
	t.Setenv("APP_VOICE_ASR_REALTIME_MODEL", "asr-model-a")
	t.Setenv("APP_VOICE_TTS_LOCAL_ENDPOINT", "wss://tts.example.com/realtime")
	t.Setenv("APP_VOICE_TTS_LOCAL_MODEL", "tts-model-a")
	t.Setenv("APP_VOICE_TTS_DEFAULT_VOICE", "voice-a")
	t.Setenv("APP_VOICE_TTS_VOICES_JSON", `[{"id":"voice-a","displayName":"Voice A","provider":"provider-a"},{"id":"voice-b","displayName":"Voice B","provider":"provider-a"}]`)
}
