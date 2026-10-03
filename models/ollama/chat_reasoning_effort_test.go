package ollama

import (
	jsonv2 "encoding/json/v2"
	"strings"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
)

func TestNativeThinkOwnsBooleanAndLevelConfiguration(t *testing.T) {
	for _, value := range []any{false, true, "low", "medium", "high", "max"} {
		options := corechat.Options{}
		if err := options.Extensions.Set(RequestExtensionKey, map[string]any{"think": value}); err != nil {
			t.Fatal(err)
		}
		request, err := mapProtocolRequest(corechat.Options{Model: "qwen3"}, &corechat.Request{
			Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hi"))}, Options: options,
		})
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := jsonv2.Marshal(request.Think)
		if err != nil {
			t.Fatal(err)
		}
		want, err := jsonv2.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != string(want) {
			t.Fatalf("think = %s, want %s", encoded, want)
		}
	}
}

func TestNativeThinkRejectsUndocumentedLevel(t *testing.T) {
	options := corechat.Options{}
	if err := options.Extensions.Set(RequestExtensionKey, map[string]any{"think": "enormous"}); err != nil {
		t.Fatal(err)
	}
	if _, err := mapProtocolRequest(corechat.Options{Model: "qwen3"}, &corechat.Request{Options: options}); err == nil {
		t.Fatal("accepted an undocumented native thinking level")
	}
}

func TestNativeChatRejectsCompetingReasoningEffort(t *testing.T) {
	for _, inDefaults := range []bool{false, true} {
		for _, withNativeThink := range []bool{false, true} {
			defaults := corechat.Options{Model: "qwen3"}
			request := &corechat.Request{Options: corechat.Options{}}
			if withNativeThink {
				if err := request.Options.Extensions.Set(RequestExtensionKey, map[string]any{"think": false}); err != nil {
					t.Fatal(err)
				}
			}
			if inDefaults {
				defaults.ReasoningEffort = corechat.ReasoningEffort("high")
			} else {
				request.Options.ReasoningEffort = corechat.ReasoningEffort("high")
			}
			if _, err := mapProtocolRequest(defaults, request); err == nil || !strings.Contains(err.Error(), "options.reasoning_effort is unsupported") {
				t.Fatalf("accepted a second think owner: %v", err)
			}
		}
	}
}
