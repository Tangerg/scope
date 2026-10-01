package providerconformance_test

import (
	"testing"
	"time"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/assemblyai"
	"github.com/Tangerg/scope/models/blackforestlabs"
	"github.com/Tangerg/scope/models/gladia"
	"github.com/Tangerg/scope/models/luma"
	"github.com/Tangerg/scope/models/replicate"
	"github.com/Tangerg/scope/models/revai"
)

func TestAsyncProviderPollingDurationsShareOneContract(t *testing.T) {
	transcriptionOptions := map[string]transcription.Options{}
	for name, model := range map[string]string{
		"assemblyai": assemblyai.ModelUniversal3Point5Pro,
		"gladia":     gladia.ModelSolaria1,
		"revai":      revai.ModelMachine,
	} {
		options := transcription.Options{Model: model}
		err := options.Validate()
		if err != nil {
			t.Fatal(err)
		}
		transcriptionOptions[name] = options
	}
	imageOptions := map[string]image.Options{}
	for name, model := range map[string]string{
		"blackforestlabs": blackforestlabs.ModelFlux11Pro,
		"luma":            luma.ModelUni1,
		"replicate-image": replicate.ModelFluxSchnell,
	} {
		options := image.Options{Model: model}
		err := options.Validate()
		if err != nil {
			t.Fatal(err)
		}
		imageOptions[name] = options
	}
	speechOptions := speech.Options{Model: replicate.ModelXTTSV2}
	err := speechOptions.Validate()
	if err != nil {
		t.Fatal(err)
	}

	validators := map[string]func(time.Duration, time.Duration) error{
		"assemblyai": func(interval, timeout time.Duration) error {
			return (assemblyai.TranscriptionModelConfig{
				APIKey: "key", DefaultOptions: transcriptionOptions["assemblyai"],
				PollInterval: interval, PollTimeout: timeout,
			}).Validate()
		},
		"blackforestlabs": func(interval, timeout time.Duration) error {
			return (blackforestlabs.ImageModelConfig{
				APIKey: "key", DefaultOptions: imageOptions["blackforestlabs"],
				PollInterval: interval, PollTimeout: timeout,
			}).Validate()
		},
		"gladia": func(interval, timeout time.Duration) error {
			return (gladia.TranscriptionModelConfig{
				APIKey: "key", DefaultOptions: transcriptionOptions["gladia"],
				PollInterval: interval, PollTimeout: timeout,
			}).Validate()
		},
		"luma": func(interval, timeout time.Duration) error {
			return (luma.ImageModelConfig{
				APIKey: "key", DefaultOptions: imageOptions["luma"],
				PollInterval: interval, PollTimeout: timeout,
			}).Validate()
		},
		"replicate-image": func(interval, timeout time.Duration) error {
			return (replicate.ImageModelConfig{
				APIKey: "key", DefaultOptions: imageOptions["replicate-image"],
				InputSchema:  replicate.FluxSchnellImageInputSchema(),
				PollInterval: interval, PollTimeout: timeout,
			}).Validate()
		},
		"replicate-speech": func(interval, timeout time.Duration) error {
			return (replicate.SpeechModelConfig{
				APIKey: "key", DefaultOptions: speechOptions,
				InputSchema:  replicate.XTTSV2SpeechInputSchema(),
				PollInterval: interval, PollTimeout: timeout,
			}).Validate()
		},
		"revai": func(interval, timeout time.Duration) error {
			return (revai.TranscriptionModelConfig{
				APIKey: "key", DefaultOptions: transcriptionOptions["revai"],
				PollInterval: interval, PollTimeout: timeout,
			}).Validate()
		},
	}

	for name, validate := range validators {
		t.Run(name, func(t *testing.T) {
			if err := validate(0, 0); err != nil {
				t.Fatalf("zero values must select defaults: %v", err)
			}
			if err := validate(-time.Second, 0); err == nil {
				t.Fatal("negative PollInterval was accepted")
			}
			if err := validate(0, -time.Second); err == nil {
				t.Fatal("negative PollTimeout was accepted")
			}
		})
	}
}
