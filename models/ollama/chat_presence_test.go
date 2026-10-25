package ollama

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
)

func TestNativeToolCallRequiresArgumentsAndIdentityEvidence(t *testing.T) {
	for _, function := range []string{`{"name":"lookup","arguments":{}}`, `{"index":null,"name":"lookup","arguments":{}}`, `{"index":0,"name":"lookup"}`, `{"index":0,"name":"lookup","arguments":{}}`} {
		t.Run(function, func(t *testing.T) {
			var response nativeChatResponse
			if err := jsonv2.Unmarshal([]byte(`{"model":"test","done":true,"message":{"tool_calls":[{"function":`+function+`}]}}`), &response); err != nil {
				t.Fatal(err)
			}
			_, err := newProtocolResponseMapper().mapDelta("test", response)
			if function != `{"index":0,"name":"lookup","arguments":{}}` {
				if !errors.Is(err, corechat.ErrInvalidResponse) {
					t.Fatal(err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
