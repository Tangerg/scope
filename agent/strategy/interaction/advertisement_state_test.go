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
		WorkingContext: &Input{Messages: []chat.Message{
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
	for _, names := range [][]string{{"first", "first"}, {"second", "first"}, {"unknown"}, {"initial"}, {" first"}, {""}} {
		state := executionState{WorkingContext: &Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("restore"))}},
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

// advertisingTestTool pairs a continuing Call with an explicit deferred-Tool
// declaration, so the advertisement is a typed result of the call rather than an
// ambient context effect.
type advertisingTestTool struct {
	tool.Tool
	declare    []string
	failure    error
	panicValue any
}

func (a *advertisingTestTool) AdvertiseDeferred(context.Context, tool.Invocation, tool.Result) ([]string, error) {
	if a.panicValue != nil {
		panic(a.panicValue)
	}
	return a.declare, a.failure
}

func TestAdvertisementNormalizesLiveDeclarations(t *testing.T) {
	inner, err := tool.NewFunc(tool.FuncConfig{Name: "active", Description: "Reveal a deferred Tool set."},
		func(context.Context, struct{}) (string, error) { return "completed", nil })
	if err != nil {
		t.Fatal(err)
	}
	declared := []string{"second", "first", "second", "first"}
	executable := &advertisingTestTool{Tool: inner, declare: declared}
	dispatcher := newToolDispatcher(nil)
	for _, name := range []string{"first", "second"} {
		dispatcher.manifest.entries[name] = toolManifestEntry{deferred: true}
	}
	if bindErr := dispatcher.bindTool(executable, false); bindErr != nil {
		t.Fatal(bindErr)
	}
	prepared := dispatcher.prepareToolCall(chat.ToolCall{ID: "call", Name: "active", Arguments: `{}`}, nil)
	result, names, _, _, err := dispatcher.callTool(t.Context(), agent.EffectRequest{}, prepared)
	if err != nil || !slices.Equal(names, []string{"first", "second"}) || result.Output.Content[0].Text != "completed" {
		t.Fatalf("live declaration: names=%v result=%v error=%v", names, result, err)
	}
	if !slices.Equal(declared, []string{"second", "first", "second", "first"}) {
		t.Fatal("normalization mutated Tool-owned declaration")
	}
	merged, err := dispatcher.manifest.mergeAdvertisements([]string{"second"}, []string{"first", "second"})
	if err != nil || !slices.Equal(merged, []string{"first", "second"}) {
		t.Fatalf("merged declaration: %v, %v", merged, err)
	}
}

func TestAdvertisementRejectsNonDeferredDeclaration(t *testing.T) {
	inner, err := newContinuingTool(tool.FuncConfig{Name: "active", Description: "Exercise the advertising boundary."}, func(context.Context, tool.Invocation) (tool.Result, error) {
		return tool.Result{Output: chat.NewTextToolOutput("done")}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	executable := &advertisingTestTool{Tool: inner, declare: []string{"unknown"}}
	dispatcher := newToolDispatcher(nil)
	if err := dispatcher.bindTool(executable, false); err != nil {
		t.Fatal(err)
	}
	prepared := dispatcher.prepareToolCall(chat.ToolCall{ID: "call", Name: "active", Arguments: `{}`}, nil)
	result, names, _, _, callErr := dispatcher.callTool(t.Context(), agent.EffectRequest{}, prepared)
	if !errors.Is(callErr, ErrInvalidToolAdvertisement) || len(names) != 0 || result.ID != "call" || result.Output.Content[0].Text != "done" {
		t.Fatalf("non-deferred declaration lost completed call: result=%v names=%v err=%v", result, names, callErr)
	}
}

func TestAdvertisementCommitsOnlyForASuccessfulCall(t *testing.T) {
	for _, outcome := range []string{"success", "error", "input", "panic"} {
		t.Run(outcome, func(t *testing.T) {
			inner, err := newContinuingTool(tool.FuncConfig{Name: "active", Description: "Exercise the advertising boundary."}, func(_ context.Context, _ tool.Invocation) (tool.Result, error) {
				switch outcome {
				case "error":
					return tool.Result{}, errors.New("unknown outcome")
				case "input":
					return waitingInput([]byte(`"continue?"`), []byte(`{"type":"boolean"}`), []byte(`null`))
				case "panic":
					panic("unknown outcome")
				default:
					return tool.Result{Output: chat.NewTextToolOutput("done")}, nil
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			executable := &advertisingTestTool{Tool: inner, declare: []string{"deferred"}}
			dispatcher := newToolDispatcher(nil)
			dispatcher.manifest.entries["deferred"] = toolManifestEntry{deferred: true}
			if err := dispatcher.bindTool(executable, false); err != nil {
				t.Fatal(err)
			}
			prepared := dispatcher.prepareToolCall(chat.ToolCall{ID: "call", Name: "active", Arguments: `{}`}, nil)
			_, names, required, _, callErr := dispatcher.callTool(t.Context(), agent.EffectRequest{}, prepared)
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
