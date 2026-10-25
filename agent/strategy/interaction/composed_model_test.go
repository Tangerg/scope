package interaction_test

import (
	"context"
	"errors"
	"iter"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/core/history"
	"github.com/Tangerg/scope/core/tool"
)

type composedModelObserver struct {
	settled chan interaction.ModelSettlement
}

func (*composedModelObserver) OnModelStarted(context.Context, interaction.ModelInvocation, *chat.Request) {
}

func (c *composedModelObserver) OnModelSettled(_ context.Context, _ interaction.ModelInvocation, settlement interaction.ModelSettlement) {
	c.settled <- settlement
}

func TestModelEvidenceRejectsNonExecutionWithACompletedResponse(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "call"
		if streaming {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			cause := &chat.CallFailure{Kind: chat.CallFailureNotExecuted, Cause: errors.New("contradictory rejection")}
			observer := &composedModelObserver{settled: make(chan interaction.ModelSettlement, 1)}
			config := interaction.DispatcherConfig{Observer: observer}
			if streaming {
				config.Streamer = chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
					return func(yield func(*chat.ResponseDelta, error) bool) {
						if yield(streamTextChunk("completed", chat.FinishReasonStop), nil) {
							yield(nil, cause)
						}
					}
				})
			} else {
				config.Model = chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
					return textResponse("completed"), cause
				})
			}
			outcome := dispatchLimitedResponse(t, config)
			if chat.ClassifyFailure(outcome.err) != chat.CallFailureUnknown || !errors.Is(outcome.err, cause) {
				t.Fatalf("contradictory boundary established non-execution: %v", outcome.err)
			}
			settled := <-observer.settled
			if settled.Outcome != interaction.ModelOutcomeUnknown || settled.Response == nil || settled.Response.Output.Text() != "completed" || settled.Validate() != nil {
				t.Fatalf("completed generation was lost: %+v", settled)
			}
		})
	}
}

func TestCompositeModelDoesNotPromoteContinuationNonExecution(t *testing.T) {
	var writes, calls atomic.Int32
	executable, err := tool.NewFunc(tool.FuncConfig{Name: "write", Description: "Write one value."}, func(context.Context, struct{}) (string, error) {
		writes.Add(1)
		return "committed", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	middleware, err := chatclient.NewSingleBatchToolMiddleware(executable)
	if err != nil {
		t.Fatal(err)
	}
	observer := &composedModelObserver{settled: make(chan interaction.ModelSettlement, 1)}
	model := middleware(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		if calls.Add(1) == 1 {
			return toolBatchResponse(chat.ToolCall{ID: "write-1", Name: "write", Arguments: `{}`}), nil
		}
		return nil, &chat.CallFailure{Kind: chat.CallFailureNotExecuted, Cause: errors.New("follow-up rejected before provider work")}
	}))
	deployment := configuredInteraction(t, interaction.DefinitionConfig{Name: "composed.model", Description: "Classify one complete model invocation."}, interaction.DispatcherConfig{Model: model, Observer: observer}, interaction.ToolSetConfig{})
	store := agent.NewMemoryTreeCommitter()
	engine := publicationEngine(t, deployment, store)
	result, err := engine.Run(t.Context(), deployment.Deployment, interactionInput(t, "write"))
	if err != nil || result.Termination().Status() != agent.StatusFailed {
		t.Fatalf("result=%v error=%v", result.Termination(), err)
	}
	settled := <-observer.settled
	if settled.Outcome != interaction.ModelOutcomeFailed || settled.Validate() != nil {
		t.Fatalf("completed model and Tool work reported non-execution: %+v", settled)
	}
	head, found, err := store.LoadTree(t.Context(), result.ProcessID())
	if err != nil || !found {
		t.Fatalf("final head: found=%v error=%v", found, err)
	}
	for _, process := range head.ProcessSnapshots() {
		if len(process.UnknownEffectIDs()) != 0 {
			t.Fatal("known unsuccessful continuation became uncertain")
		}
	}
	head, err = agent.ParseTreeSnapshot(head.JSON())
	if err != nil {
		t.Fatal(err)
	}
	recovered := publicationEngine(t, deployment, store)
	process, err := recovered.RestoreTree(t.Context(), deployment.Deployment, head)
	if err != nil {
		t.Fatal(err)
	}
	result, err = process.Await(t.Context())
	if err != nil || result.Termination().Status() != agent.StatusFailed || writes.Load() != 1 || calls.Load() != 2 {
		t.Fatalf("restore=%v error=%v writes=%d model calls=%d", result.Termination(), err, writes.Load(), calls.Load())
	}
}

type postgenerationStore struct {
	uncertain bool
	writes    atomic.Int32
}

func (*postgenerationStore) Read(context.Context, history.ConversationID) ([]chat.Message, error) {
	return nil, nil
}

func (p *postgenerationStore) Write(context.Context, history.ConversationID, ...chat.Message) (history.WriteOutcome, error) {
	p.writes.Add(1)
	return history.WriteOutcome{Uncertain: p.uncertain}, errors.New("history persistence failed after generation")
}

func TestModelObservationRetainsGenerationAcrossHistoryFailure(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		for _, streaming := range []bool{false, true} {
			name := "call"
			if streaming {
				name = "stream"
			}
			if uncertain {
				name += "/unknown"
			} else {
				name += "/failed"
			}
			t.Run(name, func(t *testing.T) {
				store := &postgenerationStore{uncertain: uncertain}
				middleware, err := history.NewMiddleware(history.MiddlewareConfig{Store: store, ConversationID: "observed"})
				if err != nil {
					t.Fatal(err)
				}
				var calls atomic.Int32
				usage := &chat.ResponseMetadata{Usage: &chat.Usage{InputTokens: 17, OutputTokens: 11}}
				response := &chat.Response{Output: &chat.Output{Message: new(chat.NewAssistantMessage(chat.NewTextPart("completed generation"))), FinishReason: chat.FinishReasonStop}, Metadata: usage}
				observer := &composedModelObserver{settled: make(chan interaction.ModelSettlement, 1)}
				config := interaction.DispatcherConfig{Observer: observer}
				if streaming {
					config.Streamer = middleware.Stream(chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
						return func(yield func(*chat.ResponseDelta, error) bool) {
							calls.Add(1)
							yield(&chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta("completed generation")}, FinishReason: chat.FinishReasonStop, Metadata: usage}, nil)
						}
					}))
				} else {
					config.Model = middleware.Call(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
						calls.Add(1)
						return response.Clone(), nil
					}))
				}
				deployment := configuredInteraction(t, interaction.DefinitionConfig{Name: "history.model", Description: "Keep completed generation evidence."}, config, interaction.ToolSetConfig{})
				committer := agent.NewMemoryTreeCommitter()
				engine := publicationEngine(t, deployment, committer)
				process, err := engine.Start(t.Context(), deployment.Deployment, interactionInput(t, "ask"))
				if err != nil {
					t.Fatal(err)
				}
				settled := <-observer.settled
				outcome, status := interaction.ModelOutcomeFailed, agent.StatusFailed
				if uncertain {
					outcome, status = interaction.ModelOutcomeUnknown, agent.StatusCanceled
					if cancelErr := process.RequestCancellation(t.Context(), "retain unresolved history write"); cancelErr != nil {
						t.Fatal(cancelErr)
					}
				}
				if settled.Outcome != outcome || settled.Failure == "" || !reflect.DeepEqual(settled.Response, response) || settled.Validate() != nil {
					t.Fatalf("generation or postprocessing fact changed: %+v", settled)
				}
				result, err := process.Await(t.Context())
				if err != nil || result.Termination().Status() != status {
					t.Fatalf("postprocessing became success: result=%v error=%v", result.Termination(), err)
				}
				if joinErr := process.Join(t.Context()); joinErr != nil {
					t.Fatal(joinErr)
				}
				head, found, err := committer.LoadTree(t.Context(), result.ProcessID())
				if err != nil || !found {
					t.Fatalf("final head: found=%v error=%v", found, err)
				}
				head, err = agent.ParseTreeSnapshot(head.JSON())
				if err != nil {
					t.Fatal(err)
				}
				for _, state := range head.ProcessSnapshots() {
					if (len(state.UnknownEffectIDs()) != 0) != uncertain {
						t.Fatal("history write certainty changed at durable settlement")
					}
				}
				recovered := publicationEngine(t, deployment, committer)
				process, err = recovered.RestoreTree(t.Context(), deployment.Deployment, head)
				if err != nil {
					t.Fatal(err)
				}
				result, err = process.Await(t.Context())
				if err != nil || result.Termination().Status() != status || calls.Load() != 1 || store.writes.Load() != 1 {
					t.Fatalf("restore=%v error=%v calls=%d writes=%d", result.Termination(), err, calls.Load(), store.writes.Load())
				}
			})
		}
	}
}
