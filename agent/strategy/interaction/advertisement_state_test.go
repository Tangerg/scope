package interaction

import (
	"errors"
	"slices"
	"testing"

	"github.com/Tangerg/scope/core/chat"
)

func TestAdvertisedToolNamesSurviveExecutionStateRestore(t *testing.T) {
	definition, err := NewDefinition(DefinitionConfig{
		Name: "interaction.advertisement_restore", Description: "Verify deferred Tool manifest recovery.",
		MaxModelCalls: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	state := executionState{
		Phase: phaseReadyModel,
		WorkingContext: &chat.Request{Messages: []chat.Message{
			chat.NewUserMessage(chat.NewTextPart("restore deferred manifest")),
		}},
		ModelCallCount:      1,
		AdvertisedToolNames: []string{"first", "second"},
	}
	if validateErr := state.Validate(definition); validateErr != nil {
		t.Fatal(validateErr)
	}
	encoded, err := encodeState(state)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := definition.Restore(encoded)
	if err != nil {
		t.Fatal(err)
	}
	restoredExecution, ok := restored.(*execution)
	if !ok {
		t.Fatalf("restored Execution type = %T", restored)
	}
	if !slices.Equal(restoredExecution.state.AdvertisedToolNames, state.AdvertisedToolNames) {
		t.Fatalf(
			"restored advertised Tools = %v, want %v",
			restoredExecution.state.AdvertisedToolNames,
			state.AdvertisedToolNames,
		)
	}
	restoredExecution.state.AdvertisedToolNames[0] = "mutated"
	if state.AdvertisedToolNames[0] != "first" {
		t.Fatal("restored Execution aliases source state")
	}
}

func TestExecutionStateRejectsDuplicateAdvertisedToolNames(t *testing.T) {
	definition, err := NewDefinition(DefinitionConfig{
		Name: "interaction.advertisement_validation", Description: "Reject malformed deferred Tool manifests.",
		MaxModelCalls: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	state := executionState{
		Phase: phaseReadyModel,
		WorkingContext: &chat.Request{Messages: []chat.Message{
			chat.NewUserMessage(chat.NewTextPart("validate deferred manifest")),
		}},
		AdvertisedToolNames: []string{"duplicate", "duplicate"},
	}
	if err := state.Validate(definition); !errors.Is(err, ErrInvalidExecutionState) {
		t.Fatalf("error = %v, want ErrInvalidExecutionState", err)
	}
}
