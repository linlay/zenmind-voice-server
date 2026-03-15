package config

import (
	"bufio"
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
}

type AsrProperties struct {
	Realtime RealtimeProxyProperties
}

type TtsProperties struct {
	DefaultMode string
	Local       LocalTtsProperties
	Llm         LlmTtsProperties
	Voices      VoiceCatalogProperties
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
	applyEnv(cfg)
	return cfg, nil
}

func defaults() *App {
	return &App{
		ServerPort: 11953,
		Asr: AsrProperties{
			Realtime: RealtimeProxyProperties{
				BaseURL:                "wss://dashscope.aliyuncs.com/api-ws/v1/realtime",
				Model:                  "qwen3-asr-flash-realtime",
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
				Endpoint:                 "wss://dashscope.aliyuncs.com/api-ws/v1/realtime",
				Model:                    "qwen3-tts-instruct-flash-realtime",
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
			Voices: VoiceCatalogProperties{
				DefaultVoice: "Cherry",
				Options: []VoiceOption{
					{ID: "Cherry", DisplayName: "Cherry", Provider: "dashscope"},
					{ID: "Ethan", DisplayName: "Ethan", Provider: "dashscope"},
					{ID: "Serena", DisplayName: "Serena", Provider: "dashscope"},
				},
			},
		},
	}
}

func applyEnv(cfg *App) {
	cfg.ServerPort = envInt("SERVER_PORT", cfg.ServerPort)
	cfg.Asr.Realtime.BaseURL = envString("DASHSCOPE_REALTIME_BASE_URL", cfg.Asr.Realtime.BaseURL)
	cfg.Asr.Realtime.Model = envString("DASHSCOPE_REALTIME_MODEL", cfg.Asr.Realtime.Model)
	cfg.Asr.Realtime.APIKey = envString("DASHSCOPE_API_KEY", cfg.Asr.Realtime.APIKey)
	cfg.Asr.Realtime.ConnectTimeoutMs = envInt("DASHSCOPE_REALTIME_CONNECT_TIMEOUT_MS", cfg.Asr.Realtime.ConnectTimeoutMs)
	cfg.Asr.Realtime.MaxClientEventBytes = envInt("DASHSCOPE_REALTIME_MAX_CLIENT_EVENT_BYTES", cfg.Asr.Realtime.MaxClientEventBytes)
	cfg.Asr.Realtime.MaxAppendAudioChars = envInt("DASHSCOPE_REALTIME_MAX_APPEND_AUDIO_CHARS", cfg.Asr.Realtime.MaxAppendAudioChars)
	cfg.Asr.Realtime.MaxPendingClientEvents = envInt("DASHSCOPE_REALTIME_MAX_PENDING_CLIENT_EVENTS", cfg.Asr.Realtime.MaxPendingClientEvents)
	cfg.Asr.Realtime.MaxPendingClientBytes = envInt("DASHSCOPE_REALTIME_MAX_PENDING_CLIENT_BYTES", cfg.Asr.Realtime.MaxPendingClientBytes)

	cfg.Tts.DefaultMode = envString("APP_VOICE_TTS_DEFAULT_MODE", cfg.Tts.DefaultMode)
	cfg.Tts.Local.Endpoint = envString("DASHSCOPE_TTS_ENDPOINT", cfg.Tts.Local.Endpoint)
	cfg.Tts.Local.Model = envString("DASHSCOPE_TTS_MODEL", cfg.Tts.Local.Model)
	cfg.Tts.Local.APIKey = envString("DASHSCOPE_TTS_API_KEY", envString("DASHSCOPE_API_KEY", cfg.Tts.Local.APIKey))
	cfg.Tts.Local.Mode = envString("DASHSCOPE_TTS_MODE", cfg.Tts.Local.Mode)
	cfg.Tts.Local.ResponseFormat = envString("DASHSCOPE_TTS_RESPONSE_FORMAT", cfg.Tts.Local.ResponseFormat)
	cfg.Tts.Local.SpeechRate = envFloat("DASHSCOPE_TTS_SPEECH_RATE", cfg.Tts.Local.SpeechRate)
	cfg.Tts.Local.Instructions = envString("DASHSCOPE_TTS_INSTRUCTIONS", cfg.Tts.Local.Instructions)
	cfg.Tts.Local.SessionFinishedTimeoutMs = envInt("DASHSCOPE_TTS_SESSION_FINISHED_TIMEOUT_MS", cfg.Tts.Local.SessionFinishedTimeoutMs)
	cfg.Tts.Local.LogSentChunkEnabled = envBool("DASHSCOPE_TTS_LOG_SENT_CHUNK_ENABLED", cfg.Tts.Local.LogSentChunkEnabled)

	cfg.Tts.Llm.Runner.BaseURL = envString("APP_VOICE_TTS_LLM_RUNNER_BASE_URL", cfg.Tts.Llm.Runner.BaseURL)
	cfg.Tts.Llm.Runner.AuthorizationToken = envString("APP_VOICE_TTS_LLM_RUNNER_AUTHORIZATION_TOKEN", cfg.Tts.Llm.Runner.AuthorizationToken)
	cfg.Tts.Llm.Runner.AgentKey = envString("APP_VOICE_TTS_LLM_RUNNER_AGENT_KEY", cfg.Tts.Llm.Runner.AgentKey)
	cfg.Tts.Llm.Runner.RequestTimeoutMs = envInt("APP_VOICE_TTS_LLM_RUNNER_REQUEST_TIMEOUT_MS", cfg.Tts.Llm.Runner.RequestTimeoutMs)

	cfg.Tts.Voices.DefaultVoice = envString("DASHSCOPE_TTS_DEFAULT_VOICE", cfg.Tts.Voices.DefaultVoice)
}

func (c *App) ListenAddr() string {
	return fmt.Sprintf(":%d", c.ServerPort)
}

func (r RealtimeProxyProperties) HasAPIKey() bool {
	return strings.TrimSpace(r.APIKey) != ""
}

func (l LocalTtsProperties) HasAPIKey() bool {
	return strings.TrimSpace(l.APIKey) != ""
}

func (r RunnerProperties) IsConfigured() bool {
	return strings.TrimSpace(r.BaseURL) != "" && strings.TrimSpace(r.AgentKey) != ""
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
