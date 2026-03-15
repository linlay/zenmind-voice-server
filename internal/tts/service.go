package tts

import (
	"strings"

	"zenmind-voice-server/internal/config"
	"zenmind-voice-server/internal/core"
)

type RealtimeTtsClient interface {
	OpenSession(options core.TtsRequestOptions) (TtsStreamSession, error)
}

type TtsStreamSession interface {
	AudioChan() <-chan core.AudioChunk
	DoneChan() <-chan struct{}
	ErrChan() <-chan error
	SampleRate() int
	Channels() int
	AppendText(text string)
	Finish()
	Cancel()
}

type SynthesisService struct {
	app          *config.App
	voiceCatalog *VoiceCatalog
	client       RealtimeTtsClient
}

type SessionPlan struct {
	VoiceID          string
	VoiceDisplayName string
	SampleRate       int
	Channels         int
	Session          TtsStreamSession
}

func NewSynthesisService(app *config.App, voiceCatalog *VoiceCatalog, client RealtimeTtsClient) *SynthesisService {
	return &SynthesisService{
		app:          app,
		voiceCatalog: voiceCatalog,
		client:       client,
	}
}

func (s *SynthesisService) OpenSession(requestedVoice string, requestedSpeechRate *float64) (SessionPlan, error) {
	voice, err := s.voiceCatalog.ResolveVoice(requestedVoice)
	if err != nil {
		return SessionPlan{}, err
	}

	local := s.app.Tts.Local
	instructions := firstNonBlank(voice.Instructions, local.Instructions)
	speechRate := requestedSpeechRate
	if speechRate == nil {
		defaultRate := local.SpeechRate
		speechRate = &defaultRate
	}
	session, err := s.client.OpenSession(core.TtsRequestOptions{
		Model:          local.Model,
		Voice:          voice.ID,
		Mode:           local.Mode,
		ResponseFormat: local.ResponseFormat,
		SpeechRate:     speechRate,
		Instructions:   instructions,
	})
	if err != nil {
		return SessionPlan{}, err
	}

	displayName := strings.TrimSpace(voice.DisplayName)
	if displayName == "" {
		displayName = voice.ID
	}
	return SessionPlan{
		VoiceID:          voice.ID,
		VoiceDisplayName: displayName,
		SampleRate:       session.SampleRate(),
		Channels:         session.Channels(),
		Session:          session,
	}, nil
}

func (s *SynthesisService) IsLocalConfigured() bool {
	return s.app.Tts.Local.HasAPIKey()
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
