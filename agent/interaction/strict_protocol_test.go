package interaction_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"strings"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/core/chat"
)

func TestParseModelResponseDeltaRejectsUnknownCoreMembers(t *testing.T) {
	for name, delta := range map[string]string{
		"delta":             `{"parts":[{"kind":"text","text":"hello"}],"unexpected":true}`,
		"part":              `{"parts":[{"kind":"text","text":"hello","unexpected":true}]}`,
		"response metadata": `{"metadata":{"model":"audit","unexpected":true}}`,
		"usage":             `{"metadata":{"usage":{"input_tokens":1,"unexpected":true}}}`,
		"output metadata":   `{"output_metadata":{"unexpected":true}}`,
		"media":             `{"parts":[{"kind":"media","media":{"mime":"image/png","source":{"kind":"uri","uri":"https://example.com/image"},"unexpected":true}}]}`,
		"media source":      `{"parts":[{"kind":"media","media":{"mime":"image/png","source":{"kind":"uri","uri":"https://example.com/image","unexpected":true}}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			payload := json.RawMessage(`{"response_delta":` + delta + `}`)
			if _, err := interaction.ParseModelResponseDelta(payload); !errors.Is(err, jsonv2.ErrUnknownName) {
				t.Fatalf("parse error = %v, want unknown object member", err)
			}
		})
	}
}

func TestDefinitionRestoreRejectsUnknownCoreMembers(t *testing.T) {
	definition, err := interaction.NewDefinition(interaction.DefinitionConfig{
		Name: "strict.restore", Description: "Restore current protocol values", MaxModelCalls: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	input, err := agent.EncodeInput(interaction.Input{
		Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("question"))},
	})
	if err != nil {
		t.Fatal(err)
	}
	execution, err := definition.Start(input)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := execution.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, restoreErr := definition.Restore(snapshot); restoreErr != nil {
		t.Fatalf("restore valid snapshot: %v", restoreErr)
	}
	for name, marker := range map[string]string{
		"request": `"working_context":{`,
		"message": `"messages":[{`,
		"part":    `"parts":[{`,
	} {
		t.Run(name, func(t *testing.T) {
			payload := string(snapshot.Payload())
			if !strings.Contains(payload, marker) {
				t.Fatalf("snapshot is missing %s", marker)
			}
			payload = strings.Replace(payload, marker, marker+`"unexpected":true,`, 1)
			invalid, stateErr := agent.NewExecutionState(snapshot.Kind(), json.RawMessage(payload))
			if stateErr != nil {
				t.Fatal(stateErr)
			}
			if _, restoreErr := definition.Restore(invalid); !errors.Is(restoreErr, interaction.ErrInvalidExecutionState) || !errors.Is(restoreErr, jsonv2.ErrUnknownName) {
				t.Fatalf("restore error = %v, want invalid state with unknown object member", restoreErr)
			}
		})
	}
}
