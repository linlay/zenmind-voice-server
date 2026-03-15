package tts

import "strings"

const DefaultRealtimeResponseFormat = "pcm"

func NormalizeResponseFormat(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "", "pcm", "wav", "mp3", "opus":
		if normalized == "" {
			return DefaultRealtimeResponseFormat
		}
		return normalized
	}

	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(value)), "PCM_") {
		return DefaultRealtimeResponseFormat
	}
	return DefaultRealtimeResponseFormat
}

func ParseSampleRate(responseFormat string) int {
	normalized := strings.ToUpper(strings.TrimSpace(responseFormat))
	switch {
	case strings.Contains(normalized, "16000"):
		return 16000
	case strings.Contains(normalized, "48000"):
		return 48000
	default:
		return 24000
	}
}
