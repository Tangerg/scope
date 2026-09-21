package elevenlabs_test

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/elevenlabs"
)

func ExampleAudioTranscriptionModelConfig() {
	options := transcription.Options{Model: elevenlabs.ModelScribeV2, Language: "eng"}
	_, err := elevenlabs.NewAudioTranscriptionModel(context.Background(), elevenlabs.AudioTranscriptionModelConfig{
		APIKey: "example-key", DefaultOptions: options,
	})
	fmt.Println(options.Model, options.Language, err)
	// Output: scribe_v2 eng <nil>
}
