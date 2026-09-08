package mistral

import (
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
)

// Mistral's own client types the field as one of five literals — stop, length,
// model_length, error, tool_calls — or an unrecognized string. error used to
// reach Other through the default branch, which files a documented value under
// "not classified", the one thing Core reserves Other against.
func TestFinishReasonCoversEveryDocumentedValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		raw  finishReason
		want corechat.FinishReason
	}{
		{raw: "", want: ""},
		{raw: finishReasonStop, want: corechat.FinishReasonStop},
		{raw: finishReasonLength, want: corechat.FinishReasonLength},
		{raw: finishReasonModelLength, want: corechat.FinishReasonLength},
		{raw: finishReasonToolCalls, want: corechat.FinishReasonToolCalls},
		{raw: finishReasonError, want: corechat.FinishReasonOther},
	}

	for _, test := range tests {
		t.Run(string(test.raw), func(t *testing.T) {
			if got := normalizeMistralFinishReason(test.raw); got != test.want {
				t.Fatalf("normalizeMistralFinishReason(%q) = %q, want %q", test.raw, got, test.want)
			}
		})
	}
}

// Other erases which terminal state it was, so the provider's own word for it
// rides along. The streaming path already did this; the synchronous one dropped
// it, so an errored generation and a provider iteration limit arrived
// indistinguishable.
func TestNativeFinishReasonSurvivesOther(t *testing.T) {
	t.Parallel()

	for _, raw := range []finishReason{finishReasonError, "some_future_reason"} {
		t.Run(string(raw), func(t *testing.T) {
			mapped := normalizeMistralFinishReason(raw)
			if mapped != corechat.FinishReasonOther {
				t.Fatalf("normalizeMistralFinishReason(%q) = %q, want %q", raw, mapped, corechat.FinishReasonOther)
			}
			outputMetadata, err := mapMistralNativeFinishReason(raw, mapped)
			if err != nil {
				t.Fatalf("mapMistralNativeFinishReason(%q) = %v, want nil", raw, err)
			}
			if outputMetadata == nil {
				t.Fatalf("mapMistralNativeFinishReason(%q) = nil metadata, want the provider value kept", raw)
			}
			native, found, err := outputMetadata.Extra.Decode[string](nativeFinishReasonKey)
			if err != nil || !found {
				t.Fatalf("Decode(%q) = %q, %t, %v", nativeFinishReasonKey, native, found, err)
			}
			if native != string(raw) {
				t.Fatalf("native finish reason = %q, want %q", native, raw)
			}
		})
	}
}

// A portable reason carries everything the caller needs, so nothing is
// attached: an output metadata block that only ever repeats "stop" would be
// noise on every successful response.
func TestNativeFinishReasonStaysAbsentForAPortableReason(t *testing.T) {
	t.Parallel()

	for _, raw := range []finishReason{finishReasonStop, finishReasonLength, finishReasonToolCalls} {
		mapped := normalizeMistralFinishReason(raw)
		outputMetadata, err := mapMistralNativeFinishReason(raw, mapped)
		if err != nil {
			t.Fatalf("mapMistralNativeFinishReason(%q) = %v, want nil", raw, err)
		}
		if outputMetadata != nil {
			t.Fatalf("mapMistralNativeFinishReason(%q) = %#v, want nil", raw, outputMetadata)
		}
	}
}
