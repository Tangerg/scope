package interaction_test

import (
	"context"
	"errors"
	"iter"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
)

func TestModelStreamCannotRetractObservedExecution(t *testing.T) {
	cause := &chat.CallFailure{Kind: chat.CallFailureNotExecuted, Cause: errors.New("continuation rejected")}
	observer := &composedModelObserver{settled: make(chan interaction.ModelSettlement, 1)}
	outcome := dispatchLimitedResponse(t, interaction.DispatcherConfig{
		Observer: observer,
		Streamer: chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
			return func(yield func(*chat.ResponseDelta, error) bool) {
				if yield(streamTextChunk("partial generation", ""), nil) {
					yield(nil, cause)
				}
			}
		}),
	})
	if chat.ClassifyFailure(outcome.err) != chat.CallFailureUnknown || !errors.Is(outcome.err, cause) {
		t.Fatalf("observed generation became non-execution: %v", outcome.err)
	}
	settled := <-observer.settled
	if settled.Outcome != interaction.ModelOutcomeUnknown || settled.Response != nil || settled.Validate() != nil {
		t.Fatalf("partial generation established a complete response or non-execution: %+v", settled)
	}
}

func TestModelStreamPreservesTerminalEvidenceAcrossInvalidTail(t *testing.T) {
	for _, test := range []struct {
		name string
		tail *chat.ResponseDelta
	}{
		{name: "nil delta"},
		{name: "delta after completion", tail: streamTextChunk("late text", "")},
	} {
		t.Run(test.name, func(t *testing.T) {
			observer := &composedModelObserver{settled: make(chan interaction.ModelSettlement, 1)}
			outcome := dispatchLimitedResponse(t, interaction.DispatcherConfig{
				Observer: observer,
				Streamer: chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
					return func(yield func(*chat.ResponseDelta, error) bool) {
						if yield(streamTextChunk("completed", chat.FinishReasonStop), nil) {
							yield(test.tail, nil)
						}
					}
				}),
			})
			if outcome.err == nil || chat.ClassifyFailure(outcome.err) != chat.CallFailureUnknown {
				t.Fatalf("invalid stream tail became success: %v", outcome.err)
			}
			settled := <-observer.settled
			if settled.Outcome != interaction.ModelOutcomeUnknown || settled.Response == nil || settled.Response.Output.Text() != "completed" || settled.Validate() != nil {
				t.Fatalf("invalid tail retracted terminal generation: %+v", settled)
			}
		})
	}
}

func TestModelStreamPanicPreservesCompletedGeneration(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		observer := &composedModelObserver{settled: make(chan interaction.ModelSettlement, 1)}
		deployment := configuredInteraction(t,
			interaction.DefinitionConfig{Name: "stream.panic", Description: "Retain observed generation through a stream panic."},
			interaction.DispatcherConfig{Observer: observer, Streamer: chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
				return func(yield func(*chat.ResponseDelta, error) bool) {
					finish := chat.FinishReason("")
					if terminal {
						finish = chat.FinishReasonStop
					}
					if yield(streamTextChunk("observed", finish), nil) {
						panic("stream cleanup failed")
					}
				}
			})}, interaction.ToolSetConfig{},
		)
		events := &agenttest.ObservationRecorder{}
		engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter(), EventListeners: []agent.EventListener{events}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
				t.Error(closeErr)
			}
		})
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		t.Cleanup(cancel)
		process, err := engine.Start(ctx, deployment.Deployment, interactionInput(t, "observe"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanupCtx := context.WithoutCancel(t.Context())
			if killErr := process.Kill(cleanupCtx, "release unknown stream"); killErr != nil && !errors.Is(killErr, agent.ErrProcessFinished) {
				t.Error(killErr)
			}
			if joinErr := process.Join(cleanupCtx); joinErr != nil {
				t.Error(joinErr)
			}
		})
		event, err := events.AwaitEvent(ctx, func(event agent.Event) bool { return event.Name() == agent.EventEffectFinished })
		if err != nil {
			t.Fatal(err)
		}
		fact, ok := event.EffectFinished()
		if !ok || fact.SettlementStatus() != agent.SettlementStatusUnknown {
			t.Fatalf("panicking stream became a definite settlement: %+v", fact)
		}
		settled := <-observer.settled
		if settled.Outcome != interaction.ModelOutcomeUnknown || settled.Validate() != nil || (settled.Response != nil) != terminal {
			t.Fatalf("terminal=%t panic lost or fabricated generation: %+v", terminal, settled)
		}
		if terminal && settled.Response.Text() != "observed" {
			t.Fatalf("panic changed terminal generation: %+v", settled.Response)
		}
	}
}
