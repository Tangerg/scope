package mistral

import (
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
)

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
			if got := test.raw.normalized(); got != test.want {
				t.Fatalf("finishReason(%q).normalized() = %q, want %q", test.raw, got, test.want)
			}
		})
	}
}

// Other erases which terminal state it was, so the native value must remain
// distinguishable from a provider iteration limit.
func TestNativeFinishReasonSurvivesOther(t *testing.T) {
	t.Parallel()

	for _, raw := range []finishReason{finishReasonError, "some_future_reason"} {
		t.Run(string(raw), func(t *testing.T) {
			mapped := raw.normalized()
			if mapped != corechat.FinishReasonOther {
				t.Fatalf("finishReason(%q).normalized() = %q, want %q", raw, mapped, corechat.FinishReasonOther)
			}
			var metadata corechat.ResponseMetadata
			if err := raw.preserveNative(&metadata); err != nil {
				t.Fatalf("finishReason(%q).preserveNative() = %v, want nil", raw, err)
			}
			native, found, err := metadata.Extra.Decode[string](nativeFinishReasonKey)
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
// attached: a metadata entry that only ever repeats "stop" would be noise on
// every successful response.
func TestNativeFinishReasonStaysAbsentForAPortableReason(t *testing.T) {
	t.Parallel()

	for _, raw := range []finishReason{finishReasonStop, finishReasonLength, finishReasonToolCalls} {
		var metadata corechat.ResponseMetadata
		if err := raw.preserveNative(&metadata); err != nil {
			t.Fatalf("finishReason(%q).preserveNative() = %v, want nil", raw, err)
		}
		if metadata.Extra != nil {
			t.Fatalf("finishReason(%q).preserveNative() recorded %#v, want nothing", raw, metadata.Extra)
		}
	}
}
