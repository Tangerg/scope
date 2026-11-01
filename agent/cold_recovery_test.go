package agent_test

import (
	startrand "crypto/rand"

	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestTypedRecoveryFromPersistedUnknownWithoutOldHost(t *testing.T) {
	for _, modelRecovery := range []bool{true, false} {
		name := "tool"
		if modelRecovery {
			name = "model"
		}
		t.Run(name, func(t *testing.T) {
			path := persistUnknownForColdRecovery(t, modelRecovery)
			store := openPublicationStore(t, path)
			defer store.Close()
			snapshot, err := run.ParseTreeSnapshot(store.tree().JSON())
			if err != nil {
				t.Fatal(err)
			}
			var modelCalls atomic.Int32
			deployment, _, tools, executable := coldRecoveryDeployment(t, modelRecovery, &modelCalls)
			engine := publicationEngine(t, deployment, store)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			root, err := engine.Recover(ctx, deployment.Program, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			var request run.EffectRequest
			for _, process := range snapshot.ProcessSnapshots() {
				for _, id := range process.UnknownEffectIDs() {
					if request.Valid() {
						t.Fatal("more than one unknown Effect")
					}
					var found bool
					request, found = snapshot.EffectRequest(process.Relation().ProcessID(), id)
					if !found || !request.Valid() || request.ID() != id || request.Relation().ProcessID() != process.Relation().ProcessID() ||
						request.ProgramRef() != process.ProgramRef() || request.Relation() != process.Relation() ||
						request.StepSequence() == 0 || request.BatchIndex() != 0 {
						t.Fatal("snapshot lost the frozen request identity")
					}
					if incarnation, ok := request.TreeIncarnationID(); !ok || incarnation != snapshot.IncarnationID() {
						t.Fatal("request lost its captured writer identity")
					}
				}
			}
			if !request.Valid() {
				t.Fatal("persisted Unknown was not retained")
			}
			for _, missing := range []struct {
				snapshot run.TreeSnapshot
				process  run.ProcessID
				effect   run.EffectID
			}{
				{run.TreeSnapshot{}, request.Relation().ProcessID(), request.ID()},
				{snapshot, run.ProcessID{}, request.ID()},
				{snapshot, request.Relation().ProcessID(), run.EffectID{}},
			} {
				if got, found := missing.snapshot.EffectRequest(missing.process, missing.effect); found || got.Valid() {
					t.Fatal("returned a request absent from the snapshot")
				}
			}
			current, err := engine.CaptureTree(ctx, root.Relation().ProcessID())
			if err != nil {
				t.Fatal(err)
			}
			if current.IncarnationID() == snapshot.IncarnationID() {
				t.Fatal("restoration did not fence the old writer")
			}
			var settlement run.Settlement
			var reference agent.ToolCallRef
			if modelRecovery {
				if _, attributionErr := agent.ToolInvocationFromRequest(request); !errors.Is(attributionErr, agent.ErrInvalidProtocol) {
					t.Fatalf("model request accepted as Tool attribution: %v", attributionErr)
				}
				settlement, err = agent.SettleModelResult(deployment.Program, request, textResponse("recovered"))
			} else {
				invocation, attributionErr := agent.ToolInvocationFromRequest(request)
				if attributionErr != nil || !invocation.Valid() {
					t.Fatalf("recovered Tool attribution: %v", attributionErr)
				}
				var attributed bool
				reference, attributed = invocation.Reference()
				if !attributed || reference.ProcessID() != root.Relation().ProcessID() || reference.ModelCallSequence() != 1 || reference.ToolCallIndex() != 0 {
					t.Fatalf("recovered logical reference=%v, present=%v", reference, attributed)
				}
				if _, dispatched := invocation.AttemptID(); dispatched {
					t.Fatal("recovered logical request invented a physical attempt")
				}
				settlement, err = tools.SettleToolResult(request, tool.NewTextOutput("recovered"), agent.ResultSucceeded, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			owner, found := engine.Process(request.Relation().ProcessID())
			if !found {
				t.Fatal("unknown owner was not restored")
			}
			if resolveErr := owner.ResolveUnknownEffect(ctx, request.ID(), settlement); resolveErr != nil {
				t.Fatal(resolveErr)
			}
			result, err := root.Await(ctx)
			if err != nil || result.Termination().Status() != run.StatusCompleted {
				t.Fatalf("status=%s error=%v", result.Termination().Status(), err)
			}
			if joinErr := root.Join(ctx); joinErr != nil {
				t.Fatal(joinErr)
			}
			payload, _ := result.Termination().Output()
			output, err := payload.Decode[agent.Output]()
			if err != nil {
				t.Fatal(err)
			}
			if modelRecovery {
				if output.ModelResponse == nil || output.ModelResponse.Output.Message.Text() != "recovered" {
					t.Fatal("investigated model output was lost")
				}
			} else if len(output.DirectToolResults) != 1 || output.DirectToolResults[0].Output.Content[0].Text != "recovered" {
				t.Fatal("investigated direct Tool output was lost")
			}
			if modelCalls.Load() != 0 || executable.calls.Load() != 0 {
				t.Fatal("cold recovery replayed external work")
			}
			if !modelRecovery {
				if len(store.database.Results) != 1 || store.database.Results[0].Reference != reference {
					t.Fatal("resolved result changed the recovered logical reference")
				}
			}
		})
	}
}

// Only the durable file crosses this boundary; no old request, binding, Runtime,
// or storage object can provide recovery evidence to the new Host.
func persistUnknownForColdRecovery(t *testing.T, modelRecovery bool) string {
	t.Helper()
	store := newPublicationStore(t)
	defer store.Close()
	loss := errors.New("unknown acknowledgment lost")
	store.after = func(snapshot run.TreeSnapshot, _ []agent.RoundResults) error {
		for _, process := range snapshot.ProcessSnapshots() {
			if len(process.UnknownEffectIDs()) != 0 {
				return loss
			}
		}
		return nil
	}
	var calls atomic.Int32
	deployment, _, _, executable := coldRecoveryDeployment(t, modelRecovery, &calls)
	engine, err := run.New(t.Context(), run.Config{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close(context.WithoutCancel(t.Context()))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	root, err := engine.Start(ctx, deployment.Program, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: interactionInput(t, "original")})
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Join(ctx); !errors.Is(err, loss) {
		t.Fatalf("expected lost acknowledgment: %v", err)
	}
	if calls.Load() != 1 || (!modelRecovery && executable.calls.Load() != 1) {
		t.Fatal("fixture did not execute the uncertain external call")
	}
	return store.path
}

func coldRecoveryDeployment(t *testing.T, modelRecovery bool, calls *atomic.Int32) (interactionDeployment, *agent.Dispatcher, agent.ToolSet, *recoveryTool) {
	t.Helper()
	executable := &recoveryTool{name: "uncertain", unknown: true}
	tools := testToolSet(t, agent.ToolSetConfig{Tools: []tool.Tool{directTool{Tool: executable}}})
	definition, err := agent.NewDefinition(agent.DefinitionConfig{PrepareContext: true,
		Name: "interaction.cold-recovery", Description: "Recover from durable Unknown evidence.",
		Tools: tools, MaxModelCalls: run.NewQuota(1),
		CompletionValidator: func(_ context.Context, candidate agent.CompletionCandidate) (agent.CompletionDecision, error) {
			if modelRecovery {
				messages := candidate.WorkingContext().Messages
				if len(messages) != 1 || messages[0].Text() != "reduced" {
					return agent.CompletionDecision{}, errors.New("effective context lost in cold recovery")
				}
			}
			return agent.CompletionDecision{Accepted: true}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := agent.NewDispatcher(definition, agent.DispatcherConfig{
		ContextProcessor: fixedResponseContext{text: "reduced"},
		Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
			calls.Add(1)
			if modelRecovery {
				return nil, errors.New("response lost")
			}
			return toolCallResponse(chat.ToolCall{ID: "call", Name: "uncertain", Arguments: "{}"}), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: run.ComputeDigest([]byte("cold-recovery-code")),
		ConfigurationDigest:  run.ComputeDigest([]byte("cold-recovery-config")),
	})
	if err != nil {
		t.Fatal(err)
	}
	return toolInteractionDeployment(deployment, tools), dispatcher, tools, executable
}
