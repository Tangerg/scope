package mcp

import (
	"crypto/sha256"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"

	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/tool"
)

func TestContinuationCannotMisrouteOrAlterElicitationActions(t *testing.T) {
	r := remoteTool{binding: "bound"}
	schema, err := jsonschema.Parse([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := remoteCheckpoint{Binding: "bound", Args: sha256.Sum256(nil), RequestID: "form", State: "opaque"}
	exact, err := jsonv2.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		state, answer string
		allowed       bool
	}{
		{`"old foreign checkpoint"`, `{"action":"cancel"}`, false},
		{string(exact), `{"action":"cancel"}`, true}, {string(exact), `{"action":"decline"}`, true},
		{string(exact), `{"action":"decline","content":{"x":2}}`, false}, {string(exact), `{"action":"accept"}`, false},
		{string(exact), `{"action":"something-else"}`, false}, {string(exact), `"not an action"`, false},
	} {
		req, buildErr := tool.NewInputRequest(tool.InputRequestConfig{Prompt: json.RawMessage(`"city?"`), ResponseSchema: schema, ContinuationState: json.RawMessage(tc.state)})
		if buildErr != nil {
			t.Fatal(buildErr)
		}
		c, answerErr := req.Continuation(json.RawMessage(tc.answer))
		if answerErr != nil {
			t.Fatal(answerErr)
		}
		var params sdkmcp.CallToolParams
		resumeErr := r.resume(&params, tool.Invocation{}, c)
		if tc.allowed {
			if resumeErr != nil || params.RequestState != "opaque" || params.InputResponses["form"] == nil {
				t.Fatalf("lost action: %v %v", params, resumeErr)
			}
		} else if resumeErr == nil || params.InputResponses != nil {
			t.Fatalf("invalid action reached the remote: %v", tc)
		}
	}
	if !relocatesSchema(map[string]any{"oneOf": []any{map[string]any{"$ref": "#/$defs/record"}}}) {
		t.Fatal("local reference would be rebound")
	}
	if errors.Is(ErrContinuationBinding, ErrIncompleteResult) {
		t.Fatal("misrouting and an unfinished result are distinct")
	}
}
