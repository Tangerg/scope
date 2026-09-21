package interaction_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
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
			snapshot, err := agent.ParseTreeSnapshot(store.tree().JSON())
			if err != nil {
				t.Fatal(err)
			}
			var modelCalls atomic.Int32
			deployment, dispatcher, tools, executable := coldRecoveryDeployment(t, modelRecovery, &modelCalls)
			engine := publicationEngine(t, deployment, store)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			root, err := engine.RestoreTree(ctx, deployment.Deployment, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			var request agent.EffectRequest
			for _, process := range snapshot.ProcessSnapshots() {
				for _, id := range process.UnknownEffectIDs() {
					if request.Valid() {
						t.Fatal("more than one unknown Effect")
					}
					var found bool
					request, found = snapshot.EffectRequest(process.ProcessID(), id)
					if !found || !request.Valid() || request.ID() != id || request.ProcessID() != process.ProcessID() ||
						request.DeploymentRef() != process.DeploymentRef() || request.Relation() != process.Relation() ||
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
				snapshot agent.TreeSnapshot
				process  agent.ProcessID
				effect   agent.EffectID
			}{
				{agent.TreeSnapshot{}, request.ProcessID(), request.ID()},
				{snapshot, agent.ProcessID{}, request.ID()},
				{snapshot, request.ProcessID(), agent.EffectID{}},
			} {
				if got, found := missing.snapshot.EffectRequest(missing.process, missing.effect); found || got.Valid() {
					t.Fatal("returned a request absent from the snapshot")
				}
			}
			current, err := engine.CaptureTree(ctx, root.ID())
			if err != nil {
				t.Fatal(err)
			}
			if current.IncarnationID() == snapshot.IncarnationID() {
				t.Fatal("restoration did not fence the old writer")
			}
			var settlement agent.Settlement
			if modelRecovery {
				settlement, err = dispatcher.SettleModelResult(request, textResponse("recovered"),
					[]chat.Message{chat.NewUserMessage(chat.NewTextPart("reduced"))})
			} else {
				settlement, err = tools.SettleToolResult(request, chat.ToolResult{
					ID: "call", Name: "uncertain", Output: chat.NewTextToolOutput("recovered"),
				}, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			owner, found := engine.Process(request.ProcessID())
			if !found {
				t.Fatal("unknown owner was not restored")
			}
			if resolveErr := owner.ResolveUnknownEffect(ctx, settlement); resolveErr != nil {
				t.Fatal(resolveErr)
			}
			result, err := root.Await(ctx)
			if err != nil || result.Status() != agent.StatusCompleted {
				t.Fatalf("status=%s error=%v", result.Status(), err)
			}
			if joinErr := root.Join(ctx); joinErr != nil {
				t.Fatal(joinErr)
			}
			payload, _ := result.Output()
			output, err := payload.Decode[interaction.Output]()
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
		})
	}
}

// Only the durable file crosses this boundary; no old request, binding, Engine,
// or storage object can provide recovery evidence to the new Host.
func persistUnknownForColdRecovery(t *testing.T, modelRecovery bool) string {
	t.Helper()
	store := newPublicationStore(t)
	defer store.Close()
	loss := errors.New("unknown acknowledgment lost")
	store.after = func(snapshot agent.TreeSnapshot, _ []interaction.RoundResults) error {
		for _, process := range snapshot.ProcessSnapshots() {
			if len(process.UnknownEffectIDs()) != 0 {
				return loss
			}
		}
		return nil
	}
	var calls atomic.Int32
	deployment, _, _, executable := coldRecoveryDeployment(t, modelRecovery, &calls)
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: store, DeploymentResolver: deployment.resolver})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close(context.WithoutCancel(t.Context()))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	root, err := engine.Start(ctx, deployment.Deployment, interactionInput(t, "original"))
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

func coldRecoveryDeployment(t *testing.T, modelRecovery bool, calls *atomic.Int32) (interactionDeployment, *interaction.Dispatcher, interaction.ToolSet, *recoveryTool) {
	t.Helper()
	executable := &recoveryTool{name: "uncertain", unknown: true}
	tools := testToolSet(t, interaction.ToolSetConfig{Tools: []tool.Tool{directTool{Tool: executable}}})
	definition, err := interaction.NewDefinition(interaction.DefinitionConfig{
		Name: "interaction.cold-recovery", Description: "Recover from durable Unknown evidence.",
		Tools: tools, MaxModelCalls: agent.NewQuota(1),
		CompletionValidator: func(_ context.Context, candidate interaction.CompletionCandidate) (interaction.CompletionDecision, error) {
			if modelRecovery {
				messages := candidate.WorkingContext().Messages
				if len(messages) != 1 || messages[0].Text() != "reduced" {
					return interaction.CompletionDecision{}, errors.New("effective context lost in cold recovery")
				}
			}
			return interaction.CompletionDecision{Accepted: true}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := interaction.NewDispatcher(definition, interaction.DispatcherConfig{
		ModelContextReducer: fixedResponseContext{text: "reduced"},
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
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("cold-recovery-code")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("cold-recovery-config")),
	})
	if err != nil {
		t.Fatal(err)
	}
	return toolInteractionDeployment(deployment, tools), dispatcher, tools, executable
}
