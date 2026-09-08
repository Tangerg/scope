package ollama

import (
	"encoding/json"
	"strings"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
)

func reasoningRequest(t *testing.T, effort corechat.ReasoningEffort) (*nativeChatRequest, error) {
	t.Helper()

	return mapProtocolRequest(
		corechat.Options{Model: "qwen3"},
		&corechat.Request{
			Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hi"))},
			Options:  corechat.Options{ReasoningEffort: effort},
		},
		false,
	)
}

// /api/chat documents think as "a boolean or a thinking level (low, medium,
// high, max)", which is what a reasoning effort names — so the portable option
// reaches the daemon. The wire field and its vocabulary already existed here;
// nothing set them, so a caller's reasoning effort was accepted and then
// dropped on the way out.
func TestReasoningEffortReachesThink(t *testing.T) {
	t.Parallel()

	for _, level := range []string{"low", "medium", "high", "max"} {
		t.Run(level, func(t *testing.T) {
			request, err := reasoningRequest(t, corechat.ReasoningEffort(level))
			if err != nil {
				t.Fatalf("mapProtocolRequest() = %v, want nil", err)
			}
			if request.Think == nil {
				t.Fatal("Think = nil, wanted the reasoning effort carried")
			}
			encoded, err := json.Marshal(request.Think)
			if err != nil {
				t.Fatal(err)
			}
			if want := `"` + level + `"`; string(encoded) != want {
				t.Fatalf("Think = %s, want %s", encoded, want)
			}
		})
	}
}

// A level the daemon does not document fails here rather than as an opaque
// provider error, and the vocabulary lives in one place so the wire decoder
// cannot disagree with this mapping.
func TestReasoningEffortRefusesAnUndocumentedLevel(t *testing.T) {
	t.Parallel()

	_, err := reasoningRequest(t, "enormous")
	if err == nil {
		t.Fatal("mapProtocolRequest() = nil error, want an invalid-level error")
	}
	if !strings.Contains(err.Error(), "high, medium, low, max") {
		t.Fatalf("mapProtocolRequest() = %v, want the accepted levels named", err)
	}
}

// Empty means "the model's default", so it must not overwrite a think the
// caller set natively — including the boolean form, which no portable effort
// can express.
func TestEmptyReasoningEffortLeavesNativeThinkAlone(t *testing.T) {
	t.Parallel()

	options := corechat.Options{}
	if err := options.Extensions.Set(RequestExtensionKey, map[string]any{"think": false}); err != nil {
		t.Fatal(err)
	}
	request, err := mapProtocolRequest(
		corechat.Options{Model: "qwen3"},
		&corechat.Request{
			Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hi"))},
			Options:  options,
		},
		false,
	)
	if err != nil {
		t.Fatalf("mapProtocolRequest() = %v, want nil", err)
	}
	if request.Think == nil {
		t.Fatal("Think = nil, want the natively set value preserved")
	}
	encoded, err := json.Marshal(request.Think)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != "false" {
		t.Fatalf("Think = %s, want false", encoded)
	}
}
