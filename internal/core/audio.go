package core

import "fmt"

type AudioChunk struct {
	PCM16LE    []byte
	SampleRate int
	Channels   int
}

func NewAudioChunk(pcm []byte, sampleRate, channels int) (AudioChunk, error) {
	if sampleRate <= 0 {
		return AudioChunk{}, fmt.Errorf("sampleRate must be > 0")
	}
	if channels <= 0 {
		return AudioChunk{}, fmt.Errorf("channels must be > 0")
	}
	return AudioChunk{
		PCM16LE:    pcm,
		SampleRate: sampleRate,
		Channels:   channels,
	}, nil
}
