package core

type TtsRequestOptions struct {
	Model          string
	Voice          string
	Mode           string
	ResponseFormat string
	SpeechRate     *float64
	Instructions   string
}
