package agent_test

import (
	startrand "crypto/rand"

	"context"
	"errors"
	"iter"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
)

type composedModelObserver struct {
	settled chan agent.ModelSettlement
}

func (*composedModelObserver) OnModelStarted(context.Context, agent.ModelInvocation, *chat.Request) {
}

func (c *composedModelObserver) OnModelSettled(_ context.Context, _ agent.ModelInvocation, settlement agent.ModelSettlement) {
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
			observer := &composedModelObserver{settled: make(chan agent.ModelSettlement, 1)}
			config := agent.DispatcherConfig{Observer: observer}
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
			outcome := dispatchLimitedResponse(t, config, 0)
			if chat.ClassifyFailure(outcome.err) != chat.CallFailureUnknown || !errors.Is(outcome.err, cause) {
				t.Fatalf("contradictory boundary established non-execution: %v", outcome.err)
			}
			settled := <-observer.settled
			if settled.Outcome != agent.ModelOutcomeUnknown || settled.Response == nil || settled.Response.Output.Text() != "completed" || settled.Validate() != nil {
				t.Fatalf("completed generation was lost: %+v", settled)
			}
		})
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
				observer := &composedModelObserver{settled: make(chan agent.ModelSettlement, 1)}
				config := agent.DispatcherConfig{Observer: observer}
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
				deployment := configuredInteraction(t, agent.DefinitionConfig{Name: "history.model", Description: "Keep completed generation evidence."}, config, agent.ToolSetConfig{})
				committer := run.NewMemoryStore()
				engine := publicationEngine(t, deployment, committer)
				process, err := engine.Start(t.Context(), deployment.Program, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: interactionInput(t, "ask")})
				if err != nil {
					t.Fatal(err)
				}
				settled := <-observer.settled
				outcome, status := agent.ModelOutcomeFailed, run.StatusFailed
				if uncertain {
					outcome, status = agent.ModelOutcomeUnknown, run.StatusCanceled
					if cancelErr := process.Cancel(t.Context(), "retain unresolved history write"); cancelErr != nil {
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
				head, err = run.ParseTreeSnapshot(head.JSON())
				if err != nil {
					t.Fatal(err)
				}
				for _, state := range head.ProcessSnapshots() {
					if (len(state.UnknownEffectIDs()) != 0) != uncertain {
						t.Fatal("history write certainty changed at durable settlement")
					}
				}
				recovered := publicationEngine(t, deployment, committer)
				process, err = recovered.Recover(t.Context(), deployment.Program, head)
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
