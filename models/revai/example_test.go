package revai_test

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/revai"
)

func ExampleTranscriptionModelConfig() {
	options := transcription.Options{Model: revai.ModelMachine, Language: "en"}
	_, err := revai.NewTranscriptionModel(context.Background(), revai.TranscriptionModelConfig{
		APIKey: "example-key", DefaultOptions: options,
	})
	fmt.Println(options.Model, options.Language, err)
	// Output: machine en <nil>
}
