package interaction_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
)

func TestPublicationResolutionRequiresReceiptIdentityAndContent(t *testing.T) {
	for _, corruption := range []string{"none", "outer_identity", "inner_identity", "digest"} {
		t.Run(corruption, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var receipt interaction.ResultReceipt
				modelCalls := 0
				deployment := configuredInteraction(t, interaction.DefinitionConfig{Name: "receipt.identity", Description: "Bind recovered receipts to their publication.", MaxModelCalls: 2}, interaction.DispatcherConfig{
					Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
						modelCalls++
						if modelCalls == 1 {
							return toolCallResponse(chat.ToolCall{ID: "missing", Name: "missing", Arguments: `{}`}), nil
						}
						return textResponse("done"), nil
					}),
					ResultCommitter: &resultCommitter{commit: func(_ context.Context, batch interaction.ResultBatch) (interaction.ResultReceipt, error) {
						receipt = batch.Receipt()
						return interaction.ResultReceipt{}, errors.New("receipt store unavailable")
					}},
				}, interaction.ToolSetConfig{})
				engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: deployment.resolver})
				if err != nil {
					t.Fatal(err)
				}
				root, err := engine.Start(t.Context(), deployment.Deployment, interactionInput(t, "work"))
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if killErr := root.Kill(t.Context(), "test complete"); killErr != nil && !errors.Is(killErr, agent.ErrProcessFinished) {
						t.Error(killErr)
					}
					if _, awaitErr := root.Await(t.Context()); awaitErr != nil {
						t.Error(awaitErr)
					}
					if joinErr := root.Join(t.Context()); joinErr != nil {
						t.Error(joinErr)
					}
					if closeErr := engine.Close(t.Context()); closeErr != nil {
						t.Error(closeErr)
					}
				}()
				synctest.Wait()
				ids := inspectProcessSnapshot(t, engine, root).UnknownEffectIDs()
				if len(ids) != 1 || ids[0] != receipt.EffectID || modelCalls != 1 {
					t.Fatalf("ids=%v calls=%d", ids, modelCalls)
				}
				outer := receipt.EffectID
				foreign, err := agent.ParseEffectID("effect:another-publication")
				if err != nil {
					t.Fatal(err)
				}
				switch corruption {
				case "outer_identity":
					outer = foreign
				case "inner_identity":
					receipt.EffectID = foreign
				case "digest":
					receipt.Digest = agent.ComputeDigest([]byte("different results"))
				}
				encoded, err := receipt.Settlement()
				if err != nil {
					t.Fatal(err)
				}
				resolution, err := agent.NewSettlement(outer, agent.SettlementStatusSucceeded, encoded.Payload())
				if err != nil {
					t.Fatal(err)
				}
				// Exercise the same public wire boundary as an adapter that re-envelopes a stored receipt.
				wire, err := json.Marshal(resolution)
				if err != nil {
					t.Fatal(err)
				}
				if decodeErr := json.Unmarshal(wire, &resolution); decodeErr != nil {
					t.Fatal(decodeErr)
				}
				err = root.ResolveUnknownEffect(t.Context(), resolution)
				if corruption == "outer_identity" {
					if !errors.Is(err, agent.ErrEffectNotPending) {
						t.Fatalf("foreign outer identity: %v", err)
					}
					if len(inspectProcessSnapshot(t, engine, root).UnknownEffectIDs()) != 1 {
						t.Fatal("rejection changed the unknown publication")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				result, err := root.Await(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if corruption == "none" {
					if result.Status() != agent.StatusCompleted || modelCalls != 2 {
						t.Fatalf("valid receipt: status=%s calls=%d", result.Status(), modelCalls)
					}
				} else if result.Status() != agent.StatusFailed || modelCalls != 1 {
					t.Fatalf("invalid receipt authorized adoption: status=%s calls=%d", result.Status(), modelCalls)
				}
			})
		})
	}
}
