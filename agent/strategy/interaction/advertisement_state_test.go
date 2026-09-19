package interaction

import (
	"context"
	"errors"
	"slices"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestAdvertisedToolNamesSurviveExecutionStateRestore(t *testing.T) {
	definition := advertisementTestDefinition(t)
	state := executionState{
		Phase: phaseReadyModel,
		WorkingContext: &chat.Request{Messages: []chat.Message{
			chat.NewUserMessage(chat.NewTextPart("restore deferred manifest")),
		}},
		ModelCallCount:      1,
		AdvertisedToolNames: []string{"first", "second"},
	}
	if validateErr := state.validate(t.Context(), definition); validateErr != nil {
		t.Fatal(validateErr)
	}
	encoded, err := state.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := definition.Restore(t.Context(), encoded)
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

func TestRestoreRejectsInvalidAdvertisements(t *testing.T) {
	definition := advertisementTestDefinition(t)
	for _, names := range [][]string{{"first", "first"}, {"unknown"}, {"initial"}, {" first"}, {""}} {
		state := executionState{Phase: phaseReadyModel,
			WorkingContext:      &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("restore"))}},
			AdvertisedToolNames: names,
		}
		encoded, err := state.snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := definition.Restore(t.Context(), encoded); !errors.Is(err, ErrInvalidExecutionState) {
			t.Fatalf("Restore accepted advertisements %v: %v", names, err)
		}
	}
}

func advertisementTestDefinition(t testing.TB) *Definition {
	t.Helper()
	var executables []tool.Tool
	for _, name := range []string{"initial", "first", "second", "existing"} {
		executable, err := tool.NewFunc(tool.FuncConfig{Name: name, Description: "Exercise deferred tool recovery."},
			func(context.Context, struct{}) (string, error) { return "done", nil })
		if err != nil {
			t.Fatal(err)
		}
		executables = append(executables, executable)
	}
	toolSet, err := NewToolSet(ToolSetConfig{
		Name: "advertisement.tools", Description: "Exercise deferred tool recovery.",
		Tools: executables[:1], DeferredTools: executables[1:],
		ImplementationDigest: agent.ComputeDigest([]byte("advertisement-tools")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("advertisement-config")),
	})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := NewDefinition(DefinitionConfig{
		Name: "interaction.advertisement_restore", Description: "Verify deferred Tool manifest recovery.",
		MaxModelCalls: agent.NewQuota(2), Tools: toolSet, ToolBudget: agent.Budget{Steps: agent.NewQuota(10), Effects: agent.NewQuota(10), Signals: agent.NewQuota(10)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func TestToolAdvertisementClosesOnEveryCallExit(t *testing.T) {
	for _, outcome := range []string{"success", "error", "input", "panic"} {
		t.Run(outcome, func(t *testing.T) {
			var saved context.Context
			executable, err := tool.NewFunc(tool.FuncConfig{Name: "active", Description: "Close the call capability."}, func(ctx context.Context, _ struct{}) (string, error) {
				saved = ctx
				accepted := make(chan error, 1)
				go func() { accepted <- AdvertiseTools(ctx, "deferred") }()
				if err := <-accepted; err != nil {
					return "", err
				}
				switch outcome {
				case "error":
					return "", errors.New("unknown outcome")
				case "input":
					return "", RequireToolInput([]byte(`"continue?"`), []byte(`{"type":"boolean"}`), []byte(`null`))
				case "panic":
					panic("unknown outcome")
				default:
					return "done", nil
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			dispatcher := &toolDispatcher{tools: make(map[string]boundTool), deferredToolNames: map[string]struct{}{"deferred": {}}}
			if err := dispatcher.bindTool(executable, false); err != nil {
				t.Fatal(err)
			}
			prepared := dispatcher.prepareToolCall(chat.ToolCall{ID: "call", Name: "active", Arguments: `{}`})
			_, names, required, _, callErr := dispatcher.callTool(t.Context(), agent.EffectRequest{}, 1, 0, prepared)
			if saved == nil {
				t.Fatal("Tool was not called")
			}
			if err := AdvertiseTools(saved, "deferred"); !errors.Is(err, ErrToolAdvertisementUnavailable) {
				t.Fatalf("closed capability returned %v", err)
			}
			switch outcome {
			case "success":
				if callErr != nil || !slices.Equal(names, []string{"deferred"}) {
					t.Fatalf("accepted advertisement lost: %v %v", names, callErr)
				}
			case "input":
				if required == nil || callErr != nil || len(names) != 0 {
					t.Fatalf("input outcome: %v %v %v", required, names, callErr)
				}
			default:
				if callErr == nil || len(names) != 0 {
					t.Fatalf("failed outcome: %v %v", names, callErr)
				}
			}
		})
	}
}

func TestToolAdvertisementCloseIsAtomicWithAdmission(t *testing.T) {
	for range 100 {
		advertiser := newToolAdvertiser(map[string]struct{}{"deferred": {}})
		ctx := withToolAdvertiser(t.Context(), advertiser)
		start := make(chan struct{})
		accepted := make(chan error, 1)
		go func() { <-start; accepted <- AdvertiseTools(ctx, "deferred") }()
		close(start)
		names := advertiser.close()
		err := <-accepted
		if err == nil {
			if !slices.Equal(names, []string{"deferred"}) {
				t.Fatalf("successful admission lost at close: %v", names)
			}
		} else if !errors.Is(err, ErrToolAdvertisementUnavailable) || len(names) != 0 {
			t.Fatalf("closed admission: names=%v error=%v", names, err)
		}
		if err := AdvertiseTools(ctx, "deferred"); !errors.Is(err, ErrToolAdvertisementUnavailable) {
			t.Fatalf("late admission: %v", err)
		}
	}
}
