package arch

import (
	"encoding/json"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/moderation"
	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/core/transcription"
)

func TestModalityWireValuesValidateAtJSONBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		marshaler   any
		unmarshaler any
	}{
		{"chat.Options", chat.Options{}, new(chat.Options)},
		{"chat.Request", chat.Request{}, new(chat.Request)},
		{"chat.OutputMetadata", chat.OutputMetadata{}, new(chat.OutputMetadata)},
		{"chat.Output", chat.Output{}, new(chat.Output)},
		{"chat.Usage", chat.Usage{}, new(chat.Usage)},
		{"chat.ResponseMetadata", chat.ResponseMetadata{}, new(chat.ResponseMetadata)},
		{"chat.Response", chat.Response{}, new(chat.Response)},

		{"embedding.Options", embedding.Options{}, new(embedding.Options)},
		{"embedding.Request", embedding.Request{}, new(embedding.Request)},
		{"embedding.Output", embedding.Output{}, new(embedding.Output)},
		{"embedding.Usage", embedding.Usage{}, new(embedding.Usage)},
		{"embedding.ResponseMetadata", embedding.ResponseMetadata{}, new(embedding.ResponseMetadata)},
		{"embedding.Response", embedding.Response{}, new(embedding.Response)},

		{"image.Options", image.Options{}, new(image.Options)},
		{"image.Request", image.Request{}, new(image.Request)},
		{"image.Output", image.Output{}, new(image.Output)},
		{"image.ResponseMetadata", image.ResponseMetadata{}, new(image.ResponseMetadata)},
		{"image.Response", image.Response{}, new(image.Response)},

		{"speech.Options", speech.Options{}, new(speech.Options)},
		{"speech.Request", speech.Request{}, new(speech.Request)},
		{"speech.Output", speech.Output{}, new(speech.Output)},
		{"speech.ResponseMetadata", speech.ResponseMetadata{}, new(speech.ResponseMetadata)},
		{"speech.Response", speech.Response{}, new(speech.Response)},

		{"transcription.Options", transcription.Options{}, new(transcription.Options)},
		{"transcription.Request", transcription.Request{}, new(transcription.Request)},
		{"transcription.Output", transcription.Output{}, new(transcription.Output)},
		{"transcription.ResponseMetadata", transcription.ResponseMetadata{}, new(transcription.ResponseMetadata)},
		{"transcription.Response", transcription.Response{}, new(transcription.Response)},

		{"moderation.Options", moderation.Options{}, new(moderation.Options)},
		{"moderation.Request", moderation.Request{}, new(moderation.Request)},
		{"moderation.Verdict", moderation.Verdict{}, new(moderation.Verdict)},
		{"moderation.Categories", moderation.Categories{}, new(moderation.Categories)},
		{"moderation.Output", moderation.Output{}, new(moderation.Output)},
		{"moderation.ResponseMetadata", moderation.ResponseMetadata{}, new(moderation.ResponseMetadata)},
		{"moderation.Response", moderation.Response{}, new(moderation.Response)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, ok := test.marshaler.(json.Marshaler); !ok {
				t.Errorf("%s does not implement json.Marshaler", test.name)
			}
			if _, ok := test.unmarshaler.(json.Unmarshaler); !ok {
				t.Errorf("*%s does not implement json.Unmarshaler", test.name)
			}
		})
	}
}
