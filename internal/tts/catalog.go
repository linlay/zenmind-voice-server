package tts

import (
	"fmt"
	"strings"

	"zenmind-voice-server/internal/config"
)

type VoiceCatalog struct {
	app *config.App
}

func NewVoiceCatalog(app *config.App) *VoiceCatalog {
	return &VoiceCatalog{app: app}
}

func (c *VoiceCatalog) ListVoices() []config.VoiceOption {
	return c.app.Tts.Voices.SortedOptions()
}

func (c *VoiceCatalog) ResolveVoice(requested string) (config.VoiceOption, error) {
	voiceID := strings.TrimSpace(requested)
	if voiceID == "" {
		voiceID = c.app.Tts.Voices.DefaultVoice
	}
	for _, option := range c.ListVoices() {
		if strings.EqualFold(option.ID, voiceID) {
			return option, nil
		}
	}
	return config.VoiceOption{}, fmt.Errorf("unsupported voice: %s", voiceID)
}

func (c *VoiceCatalog) DefaultVoiceID() string {
	return c.app.Tts.Voices.DefaultVoice
}
