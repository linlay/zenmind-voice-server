package config

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type App struct {
	ServerPort int
	Asr        AsrProperties
	Tts        TtsProperties
	WS         WSProperties
}

type WSProperties struct {
	AllowedOrigins  []string
	MaxMessageBytes int64
	MaxTasksPerConn int
	PingIntervalMs  int
	PongTimeoutMs   int
	WriteTimeoutMs  int
}

type AsrProperties struct {
	ClientGate                  ClientGateProperties
	Realtime                    RealtimeProxyProperties
	WebSocketDetailedLogEnabled bool
}

type ClientGateProperties struct {
	Enabled      bool
	RMSThreshold float64
	OpenHoldMs   int
	CloseHoldMs  int
	PreRollMs    int
}

type TtsProperties struct {
	DefaultMode                 string
	WebSocketDetailedLogEnabled bool
	Local                       LocalTtsProperties
	Llm                         LlmTtsProperties
	Voices                      VoiceCatalogProperties
}

type RealtimeProxyProperties struct {
	BaseURL                string
	Model                  string
	APIKey                 string
	ConnectTimeoutMs       int
	MaxClientEventBytes    int
	MaxAppendAudioChars    int
	MaxPendingClientEvents int
	MaxPendingClientBytes  int
}

type LocalTtsProperties struct {
	Endpoint                 string
	Model                    string
	APIKey                   string
	Mode                     string
	ResponseFormat           string
	SpeechRate               float64
	Instructions             string
	SessionFinishedTimeoutMs int
	LogSentChunkEnabled      bool
}

type LlmTtsProperties struct {
	Runner RunnerProperties
}

type RunnerProperties struct {
	BaseURL            string
	AuthorizationToken string
	AgentKey           string
	RequestTimeoutMs   int
}

type VoiceCatalogProperties struct {
	DefaultVoice string
	Options      []VoiceOption
}

type VoiceOption struct {
	ID           string
	DisplayName  string
	Provider     string
	Instructions string
}

func Load(root string) (*App, error) {
	if err := loadDotEnv(filepath.Join(root, ".env")); err != nil {
		return nil, err
	}

	cfg := defaults()
	if err := applyEnv(cfg); err != nil {
		return nil, err
	}
	if err := validate(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func defaults() *App {
	return &App{
		ServerPort: 11953,
		Asr: AsrProperties{
			ClientGate: ClientGateProperties{
				Enabled:      true,
				RMSThreshold: 0.008,
				OpenHoldMs:   120,
				CloseHoldMs:  480,
				PreRollMs:    240,
			},
			Realtime: RealtimeProxyProperties{
				ConnectTimeoutMs:       10000,
				MaxClientEventBytes:    1048576,
				MaxAppendAudioChars:    2097152,
				MaxPendingClientEvents: 128,
				MaxPendingClientBytes:  1048576,
			},
		},
		Tts: TtsProperties{
			DefaultMode: "local",
			Local: LocalTtsProperties{
				Mode:                     "server_commit",
				ResponseFormat:           "pcm",
				SpeechRate:               1.2,
				SessionFinishedTimeoutMs: 120000,
			},
			Llm: LlmTtsProperties{
				Runner: RunnerProperties{
					RequestTimeoutMs: 120000,
				},
			},
			Voices: VoiceCatalogProperties{},
		},
		WS: WSProperties{
			MaxMessageBytes: 2097152,
			MaxTasksPerConn: 16,
			PingIntervalMs:  30000,
			PongTimeoutMs:   60000,
			WriteTimeoutMs:  10000,
		},
	}
}

func applyEnv(cfg *App) error {
	cfg.ServerPort = envInt("SERVER_PORT", cfg.ServerPort)
	cfg.Asr.Realtime.BaseURL = envString("APP_VOICE_ASR_REALTIME_BASE_URL", cfg.Asr.Realtime.BaseURL)
	cfg.Asr.Realtime.Model = envString("APP_VOICE_ASR_REALTIME_MODEL", cfg.Asr.Realtime.Model)
	cfg.Asr.Realtime.APIKey = envString("APP_VOICE_ASR_REALTIME_API_KEY", cfg.Asr.Realtime.APIKey)
	cfg.Asr.Realtime.ConnectTimeoutMs = envInt("APP_VOICE_ASR_REALTIME_CONNECT_TIMEOUT_MS", cfg.Asr.Realtime.ConnectTimeoutMs)
	cfg.Asr.Realtime.MaxClientEventBytes = envInt("APP_VOICE_ASR_REALTIME_MAX_CLIENT_EVENT_BYTES", cfg.Asr.Realtime.MaxClientEventBytes)
	cfg.Asr.Realtime.MaxAppendAudioChars = envInt("APP_VOICE_ASR_REALTIME_MAX_APPEND_AUDIO_CHARS", cfg.Asr.Realtime.MaxAppendAudioChars)
	cfg.Asr.Realtime.MaxPendingClientEvents = envInt("APP_VOICE_ASR_REALTIME_MAX_PENDING_CLIENT_EVENTS", cfg.Asr.Realtime.MaxPendingClientEvents)
	cfg.Asr.Realtime.MaxPendingClientBytes = envInt("APP_VOICE_ASR_REALTIME_MAX_PENDING_CLIENT_BYTES", cfg.Asr.Realtime.MaxPendingClientBytes)
	cfg.Asr.ClientGate.Enabled = envBool("APP_VOICE_ASR_CLIENT_GATE_ENABLED", cfg.Asr.ClientGate.Enabled)
	cfg.Asr.ClientGate.RMSThreshold = envFloat("APP_VOICE_ASR_CLIENT_GATE_RMS_THRESHOLD", cfg.Asr.ClientGate.RMSThreshold)
	cfg.Asr.ClientGate.OpenHoldMs = envInt("APP_VOICE_ASR_CLIENT_GATE_OPEN_HOLD_MS", cfg.Asr.ClientGate.OpenHoldMs)
	cfg.Asr.ClientGate.CloseHoldMs = envInt("APP_VOICE_ASR_CLIENT_GATE_CLOSE_HOLD_MS", cfg.Asr.ClientGate.CloseHoldMs)
	cfg.Asr.ClientGate.PreRollMs = envInt("APP_VOICE_ASR_CLIENT_GATE_PRE_ROLL_MS", cfg.Asr.ClientGate.PreRollMs)
	cfg.Asr.WebSocketDetailedLogEnabled = envBool("APP_VOICE_ASR_WS_DETAILED_LOG_ENABLED", cfg.Asr.WebSocketDetailedLogEnabled)

	cfg.Tts.DefaultMode = envString("APP_VOICE_TTS_DEFAULT_MODE", cfg.Tts.DefaultMode)
	cfg.Tts.WebSocketDetailedLogEnabled = envBool("APP_VOICE_TTS_WS_DETAILED_LOG_ENABLED", cfg.Tts.WebSocketDetailedLogEnabled)
	cfg.Tts.Local.Endpoint = envString("APP_VOICE_TTS_LOCAL_ENDPOINT", cfg.Tts.Local.Endpoint)
	cfg.Tts.Local.Model = envString("APP_VOICE_TTS_LOCAL_MODEL", cfg.Tts.Local.Model)
	cfg.Tts.Local.APIKey = envString("APP_VOICE_TTS_LOCAL_API_KEY", cfg.Tts.Local.APIKey)
	cfg.Tts.Local.Mode = envString("APP_VOICE_TTS_LOCAL_MODE", cfg.Tts.Local.Mode)
	cfg.Tts.Local.ResponseFormat = envString("APP_VOICE_TTS_LOCAL_RESPONSE_FORMAT", cfg.Tts.Local.ResponseFormat)
	cfg.Tts.Local.SpeechRate = envFloat("APP_VOICE_TTS_LOCAL_SPEECH_RATE", cfg.Tts.Local.SpeechRate)
	cfg.Tts.Local.Instructions = envString("APP_VOICE_TTS_LOCAL_INSTRUCTIONS", cfg.Tts.Local.Instructions)
	cfg.Tts.Local.SessionFinishedTimeoutMs = envInt("APP_VOICE_TTS_LOCAL_SESSION_FINISHED_TIMEOUT_MS", cfg.Tts.Local.SessionFinishedTimeoutMs)
	cfg.Tts.Local.LogSentChunkEnabled = envBool("APP_VOICE_TTS_LOCAL_LOG_SENT_CHUNK_ENABLED", cfg.Tts.Local.LogSentChunkEnabled)

	cfg.Tts.Llm.Runner.BaseURL = envString("APP_VOICE_TTS_LLM_RUNNER_BASE_URL", cfg.Tts.Llm.Runner.BaseURL)
	cfg.Tts.Llm.Runner.AuthorizationToken = envString("APP_VOICE_TTS_LLM_RUNNER_AUTHORIZATION_TOKEN", cfg.Tts.Llm.Runner.AuthorizationToken)
	cfg.Tts.Llm.Runner.AgentKey = envString("APP_VOICE_TTS_LLM_RUNNER_AGENT_KEY", cfg.Tts.Llm.Runner.AgentKey)
	cfg.Tts.Llm.Runner.RequestTimeoutMs = envInt("APP_VOICE_TTS_LLM_RUNNER_REQUEST_TIMEOUT_MS", cfg.Tts.Llm.Runner.RequestTimeoutMs)

	cfg.Tts.Voices.DefaultVoice = envString("APP_VOICE_TTS_DEFAULT_VOICE", cfg.Tts.Voices.DefaultVoice)

	voiceOptions, err := envVoiceOptions("APP_VOICE_TTS_VOICES_JSON", cfg.Tts.Voices.Options)
	if err != nil {
		return err
	}
	cfg.Tts.Voices.Options = voiceOptions

	cfg.WS.AllowedOrigins = envStringList("APP_VOICE_WS_ALLOWED_ORIGINS", cfg.WS.AllowedOrigins)
	cfg.WS.MaxMessageBytes = envInt64("APP_VOICE_WS_MAX_MESSAGE_BYTES", cfg.WS.MaxMessageBytes)
	cfg.WS.MaxTasksPerConn = envInt("APP_VOICE_WS_MAX_TASKS_PER_CONN", cfg.WS.MaxTasksPerConn)
	cfg.WS.PingIntervalMs = envInt("APP_VOICE_WS_PING_INTERVAL_MS", cfg.WS.PingIntervalMs)
	cfg.WS.PongTimeoutMs = envInt("APP_VOICE_WS_PONG_TIMEOUT_MS", cfg.WS.PongTimeoutMs)
	cfg.WS.WriteTimeoutMs = envInt("APP_VOICE_WS_WRITE_TIMEOUT_MS", cfg.WS.WriteTimeoutMs)
	return nil
}

func validate(cfg *App) error {
	var missing []string
	if strings.TrimSpace(cfg.Asr.Realtime.BaseURL) == "" {
		missing = append(missing, "APP_VOICE_ASR_REALTIME_BASE_URL")
	}
	if strings.TrimSpace(cfg.Asr.Realtime.Model) == "" {
		missing = append(missing, "APP_VOICE_ASR_REALTIME_MODEL")
	}
	if strings.TrimSpace(cfg.Tts.Local.Endpoint) == "" {
		missing = append(missing, "APP_VOICE_TTS_LOCAL_ENDPOINT")
	}
	if strings.TrimSpace(cfg.Tts.Local.Model) == "" {
		missing = append(missing, "APP_VOICE_TTS_LOCAL_MODEL")
	}
	if strings.TrimSpace(cfg.Tts.Voices.DefaultVoice) == "" {
		missing = append(missing, "APP_VOICE_TTS_DEFAULT_VOICE")
	}
	if len(cfg.Tts.Voices.Options) == 0 {
		missing = append(missing, "APP_VOICE_TTS_VOICES_JSON")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required configuration: %s", strings.Join(missing, ", "))
	}

	defaultVoice := strings.TrimSpace(cfg.Tts.Voices.DefaultVoice)
	seenVoiceIDs := make(map[string]struct{}, len(cfg.Tts.Voices.Options))
	foundDefault := false
	for _, option := range cfg.Tts.Voices.Options {
		voiceID := strings.TrimSpace(option.ID)
		if voiceID == "" {
			return fmt.Errorf("invalid APP_VOICE_TTS_VOICES_JSON: voice id is required")
		}
		normalizedID := strings.ToLower(voiceID)
		if _, exists := seenVoiceIDs[normalizedID]; exists {
			return fmt.Errorf("invalid APP_VOICE_TTS_VOICES_JSON: duplicate voice id %q", voiceID)
		}
		seenVoiceIDs[normalizedID] = struct{}{}
		if strings.EqualFold(voiceID, defaultVoice) {
			foundDefault = true
		}
	}
	if !foundDefault {
		return fmt.Errorf("invalid configuration: APP_VOICE_TTS_DEFAULT_VOICE %q is not present in APP_VOICE_TTS_VOICES_JSON", cfg.Tts.Voices.DefaultVoice)
	}
	return nil
}

func envVoiceOptions(key string, fallback []VoiceOption) ([]VoiceOption, error) {
	value, ok := os.LookupEnv(key)
	if !ok {
		return fallback, nil
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}

	var options []VoiceOption
	if err := json.Unmarshal([]byte(value), &options); err != nil {
		return nil, fmt.Errorf("parse %s: %w", key, err)
	}
	for i := range options {
		options[i].ID = strings.TrimSpace(options[i].ID)
		options[i].DisplayName = strings.TrimSpace(options[i].DisplayName)
		options[i].Provider = strings.TrimSpace(options[i].Provider)
		options[i].Instructions = strings.TrimSpace(options[i].Instructions)
	}
	return options, nil
}

func (c *App) ListenAddr() string {
	return fmt.Sprintf(":%d", c.ServerPort)
}

func (r RealtimeProxyProperties) HasAPIKey() bool {
	return strings.TrimSpace(r.APIKey) != ""
}

func (c ClientGateProperties) Normalized() ClientGateProperties {
	defaults := defaults().Asr.ClientGate
	if c.RMSThreshold < 0 {
		c.RMSThreshold = defaults.RMSThreshold
	}
	if c.OpenHoldMs < 0 {
		c.OpenHoldMs = defaults.OpenHoldMs
	}
	if c.CloseHoldMs < 0 {
		c.CloseHoldMs = defaults.CloseHoldMs
	}
	if c.PreRollMs < 0 {
		c.PreRollMs = defaults.PreRollMs
	}
	return c
}

func (w WSProperties) IsOriginAllowed(origin string) bool {
	origin = strings.TrimSpace(origin)
	if len(w.AllowedOrigins) == 0 {
		return true
	}
	if origin == "" {
		return true
	}
	for _, allowed := range w.AllowedOrigins {
		if strings.EqualFold(strings.TrimSpace(allowed), origin) {
			return true
		}
	}
	return false
}

func (l LocalTtsProperties) HasAPIKey() bool {
	return strings.TrimSpace(l.APIKey) != ""
}

func (r RunnerProperties) IsConfigured() bool {
	return strings.TrimSpace(r.BaseURL) != ""
}

func (v VoiceCatalogProperties) SortedOptions() []VoiceOption {
	options := append([]VoiceOption(nil), v.Options...)
	sort.Slice(options, func(i, j int) bool {
		return strings.ToLower(options[i].ID) < strings.ToLower(options[j].ID)
	})
	return options
}

func loadDotEnv(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func envString(key, fallback string) string {
	value, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	return value
}

func envInt(key string, fallback int) int {
	value, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envFloat(key string, fallback float64) float64 {
	value, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func envInt64(key string, fallback int64) int64 {
	value, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func envStringList(key string, fallback []string) []string {
	value, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func envBool(key string, fallback bool) bool {
	value, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}
