package replicate

import (
	"testing"
	"time"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/speech"
)

func TestModelConfigsRejectNegativePolling(t *testing.T) {
	for _, test := range []struct {
		name              string
		interval, timeout time.Duration
	}{
		{name: "interval", interval: -time.Second},
		{name: "timeout", timeout: -time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			imageConfig := ImageModelConfig{
				APIKey:         "test-key",
				DefaultOptions: image.Options{Model: ModelFluxSchnell},
				InputSchema:    FluxSchnellImageInputSchema(),
				Polling:        invocation.Polling{Interval: test.interval, Timeout: test.timeout},
			}
			speechConfig := SpeechModelConfig{
				APIKey:         "test-key",
				DefaultOptions: speech.Options{Model: ModelXTTSV2},
				InputSchema:    XTTSV2SpeechInputSchema(),
				Polling:        invocation.Polling{Interval: test.interval, Timeout: test.timeout},
			}
			if err := imageConfig.Validate(); err == nil {
				t.Fatal("image configuration accepted a negative polling duration")
			}
			if err := speechConfig.Validate(); err == nil {
				t.Fatal("speech configuration accepted a negative polling duration")
			}
			if model, err := NewImageModel(t.Context(), imageConfig); model != nil || err == nil {
				t.Fatalf("image construction accepted a negative polling duration: model=%v error=%v", model, err)
			}
			if model, err := NewSpeechModel(t.Context(), speechConfig); model != nil || err == nil {
				t.Fatalf("speech construction accepted a negative polling duration: model=%v error=%v", model, err)
			}
		})
	}
}

func TestModelPollingDefaultsPreserveEndpointPolicy(t *testing.T) {
	for _, test := range []struct {
		name                                string
		interval, timeout                   time.Duration
		wantInterval                        time.Duration
		wantImageTimeout, wantSpeechTimeout time.Duration
	}{
		{name: "defaults", wantInterval: DefaultPollInterval, wantImageTimeout: DefaultImagePollTimeout, wantSpeechTimeout: DefaultSpeechPollTimeout},
		{name: "explicit", interval: time.Millisecond, timeout: time.Second, wantInterval: time.Millisecond, wantImageTimeout: time.Second, wantSpeechTimeout: time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			imageModel, err := NewImageModel(t.Context(), ImageModelConfig{
				APIKey:         "test-key",
				DefaultOptions: image.Options{Model: ModelFluxSchnell},
				InputSchema:    FluxSchnellImageInputSchema(),
				Polling:        invocation.Polling{Interval: test.interval, Timeout: test.timeout},
			})
			if err != nil {
				t.Fatal(err)
			}
			speechModel, err := NewSpeechModel(t.Context(), SpeechModelConfig{
				APIKey:         "test-key",
				DefaultOptions: speech.Options{Model: ModelXTTSV2},
				InputSchema:    XTTSV2SpeechInputSchema(),
				Polling:        invocation.Polling{Interval: test.interval, Timeout: test.timeout},
			})
			if err != nil {
				t.Fatal(err)
			}
			if actual := imageModel.predictions.polling; actual.Interval != test.wantInterval || actual.Timeout != test.wantImageTimeout {
				t.Fatalf("image polling=%+v, want interval=%s timeout=%s", actual, test.wantInterval, test.wantImageTimeout)
			}
			if actual := speechModel.predictions.polling; actual.Interval != test.wantInterval || actual.Timeout != test.wantSpeechTimeout {
				t.Fatalf("speech polling=%+v, want interval=%s timeout=%s", actual, test.wantInterval, test.wantSpeechTimeout)
			}
		})
	}
}
