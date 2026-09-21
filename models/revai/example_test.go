package revai_test

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/revai"
)

func ExampleAudioTranscriptionModelConfig() {
	options := transcription.Options{Model: revai.ModelMachine, Language: "en"}
	_, err := revai.NewAudioTranscriptionModel(context.Background(), revai.AudioTranscriptionModelConfig{
		APIKey: "example-key", DefaultOptions: options,
	})
	fmt.Println(options.Model, options.Language, err)
	// Output: machine en <nil>
}
