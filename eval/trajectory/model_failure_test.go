package trajectory_test

import (
	"context"
	startrand "crypto/rand"
	"sync"
	"time"

	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"iter"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/Tangerg/scope/agent"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
	"github.com/Tangerg/scope/eval/trajectory"
)

type failedModelHistory struct{}

func (failedModelHistory) Read(context.Context, history.ConversationID) ([]chat.Message, error) {
	return nil, nil
}

func (failedModelHistory) Write(context.Context, history.ConversationID, ...chat.Message) (history.WriteOutcome, error) {
	return history.WriteOutcome{}, errors.New("history commit rejected")
}

func TestHistoryFailureRetainsGenerationAndUsageInTrajectoryJSON(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "call"
		if streaming {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			middleware, err := history.NewMiddleware(history.MiddlewareConfig{Store: failedModelHistory{}, ConversationID: "recording"})
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			metadata := &chat.ResponseMetadata{Usage: &chat.Usage{InputTokens: 17, OutputTokens: 11}}
			config := agent.DispatcherConfig{}
			if streaming {
				config.Streamer = middleware.Stream(chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
					return func(yield func(*chat.ResponseDelta, error) bool) {
						calls.Add(1)
						yield(&chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta("completed generation")}, FinishReason: chat.FinishReasonStop, Metadata: metadata}, nil)
					}
				}))
			} else {
				config.Model = middleware.Call(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
					calls.Add(1)
					return &chat.Response{Output: &chat.Output{Message: new(chat.NewAssistantMessage(chat.NewTextPart("completed generation"))), FinishReason: chat.FinishReasonStop}, Metadata: metadata}, nil
				}))
			}
			recorder := new(trajectory.Recorder)
			process, _, _ := startModelRecording(t, recorder, config, run.NewMemoryStore(), false)
			result, err := process.Await(t.Context())
			if err != nil || result.Termination().Status() != run.StatusFailed {
				t.Fatalf("history failure became success: %v, %v", result.Termination(), err)
			}
			recorded, err := recorder.Take(t.Context(), process, nil)
			if err != nil {
				t.Fatal(err)
			}
			configuration := trajectoryConfig(recorded)
			configuration.Coverage = interactionCoverage(configuration.Events)
			recorded, err = trajectory.New(configuration)
			if err != nil {
				t.Fatal(err)
			}
			observed := recorded.ModelCalls()
			if len(observed) != 1 || observed[0].Outcome != agent.ModelOutcomeFailed || observed[0].Response.Text() != "completed generation" || observed[0].Failure == "" {
				t.Fatalf("generation and postprocessing evidence diverged: %+v", observed)
			}
			if tokens, accountingErr := recorded.TotalTokens(); accountingErr != nil || tokens != 28 {
				t.Fatalf("known usage=%d error=%v", tokens, accountingErr)
			}
			data, err := jsonv2.Marshal(recorded)
			if err != nil {
				t.Fatal(err)
			}
			var restored trajectory.Trajectory
			if err := jsonv2.Unmarshal(data, &restored); err != nil {
				t.Fatal(err)
			}
			if tokens, err := restored.TotalTokens(); err != nil || tokens != 28 || restored.ModelCalls()[0].Outcome != agent.ModelOutcomeFailed || calls.Load() != 1 {
				t.Fatalf("restored usage=%d error=%v model calls=%d", tokens, err, calls.Load())
			}
		})
	}
}

func startModelRecording(t *testing.T, recorder *trajectory.Recorder, config agent.DispatcherConfig, committer run.Store, replay bool) (*run.Handle[run.Payload], *run.Runtime, run.Program[run.Payload, run.Payload]) {
	t.Helper()
	return startModelRecordingWithListener(t, recorder, config, committer, replay, recorder, 0)
}

func startModelRecordingWithListener(t *testing.T, recorder *trajectory.Recorder, config agent.DispatcherConfig, committer run.Store, replay bool, listener run.EventListener, capacity int) (*run.Handle[run.Payload], *run.Runtime, run.Program[run.Payload, run.Payload]) {
	t.Helper()
	definition, err := agent.NewDefinition(agent.DefinitionConfig{Name: "test.model_recording", Description: "Record actual model boundaries.", MaxModelCalls: run.NewQuota(1)})
	if err != nil {
		t.Fatal(err)
	}
	if config.Observer == nil {
		config.Observer = recorder
	}
	dispatcher, err := agent.NewDispatcher(definition, config)
	if err != nil {
		t.Fatal(err)
	}
	var external run.Dispatcher = dispatcher
	if replay {
		external = replayableTestModelDispatcher{dispatcher}
	}
	deployment, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{Definition: definition, Dispatcher: external,
		ImplementationDigest: run.ComputeDigest([]byte("model-recording-implementation")), ConfigurationDigest: run.ComputeDigest([]byte("model-recording-configuration"))})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := run.New(t.Context(), run.Config{Store: committer, EventListeners: []run.EventListener{listener}, EventBufferCapacity: capacity})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	input, err := run.EncodePayload(agent.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("retained question"))}})
	if err != nil {
		t.Fatal(err)
	}
	process, err := engine.Start(t.Context(), deployment, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: input})
	if err != nil {
		t.Fatal(err)
	}
	return process, engine, deployment
}

func TestDefiniteModelOutcomesPreserveClassificationAndTokenEvidence(t *testing.T) {
	for _, test := range []struct {
		name       string
		cause      error
		outcome    agent.ModelOutcome
		knownUsage bool
	}{
		{name: "not executed", cause: &chat.CallFailure{Kind: chat.CallFailureNotExecuted, Cause: errors.New("input rejected")}, outcome: agent.ModelOutcomeNotExecuted, knownUsage: true},
		{name: "failed after dispatch", cause: &chat.CallFailure{Kind: chat.CallFailureFailed, Cause: errors.New("output rejected")}, outcome: agent.ModelOutcomeFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := new(trajectory.Recorder)
			cause := fmt.Errorf("model rejected request: %w", test.cause)
			model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) { return nil, cause })
			process, engine, _ := startModelRecording(t, recorder, agent.DispatcherConfig{Model: model}, run.NewMemoryStore(), false)
			result, err := process.Await(t.Context())
			if err != nil || result.Termination().Status() != run.StatusFailed {
				t.Fatalf("definite failure=%v error=%v", result.Termination(), err)
			}
			snapshot, err := engine.CaptureTree(t.Context(), process.Relation().ProcessID())
			if err != nil {
				t.Fatal(err)
			}
			for _, state := range snapshot.ProcessSnapshots() {
				if len(state.UnknownEffectIDs()) != 0 {
					t.Fatal("definite model outcome requires reconciliation")
				}
			}
			recorded, err := recorder.Take(t.Context(), process, nil)
			if err != nil {
				t.Fatal(err)
			}
			calls := recorded.ModelCalls()
			if len(calls) != 1 || calls[0].Outcome != test.outcome || calls[0].Response != nil || calls[0].Failure != cause.Error() {
				t.Fatalf("model evidence=%+v, want %s", calls, test.outcome)
			}
			finished := false
			for _, event := range recorded.Events() {
				if fact, ok := event.EffectFinished(); ok && fact.AttemptID() == calls[0].AttemptID {
					finished = fact.SettlementStatus() == run.SettlementStatusFailed
				}
			}
			if !finished {
				t.Fatal("durable effect classification differs from definite model evidence")
			}
			config := trajectoryConfig(recorded)
			config.Coverage = interactionCoverage(config.Events)
			recorded, err = trajectory.New(config)
			if err != nil {
				t.Fatal(err)
			}
			if coverageErr := recorded.CheckCoverage(); coverageErr != nil {
				t.Fatal(coverageErr)
			}
			tokens, tokenErr := recorded.TotalTokens()
			if test.knownUsage {
				if tokenErr != nil || tokens != 0 {
					t.Fatalf("unexecuted model token evidence=%d, %v", tokens, tokenErr)
				}
			} else if !errors.Is(tokenErr, trajectory.ErrIncompleteRecording) {
				t.Fatalf("a definite failure without accounting proved token usage: %d, %v", tokens, tokenErr)
			}
			data, err := jsonv2.Marshal(recorded)
			if err != nil {
				t.Fatal(err)
			}
			var restored trajectory.Trajectory
			if err := jsonv2.Unmarshal(data, &restored); err != nil || restored.ModelCalls()[0].Outcome != test.outcome {
				t.Fatalf("model outcome did not survive the current wire: %v", err)
			}
		})
	}
}

type incompleteModelObserver struct {
	recorder *trajectory.Recorder
	invalid  bool
}

func (i incompleteModelObserver) OnModelStarted(ctx context.Context, invocation agent.ModelInvocation, request *chat.Request) {
	i.recorder.OnModelStarted(ctx, invocation, request)
}

func (i incompleteModelObserver) OnModelSettled(ctx context.Context, invocation agent.ModelInvocation, _ agent.ModelSettlement) {
	if i.invalid {
		i.recorder.OnModelSettled(ctx, invocation, agent.ModelSettlement{Outcome: agent.ModelOutcomeUnobserved})
	}
}

func TestMissingOrInvalidModelObservationStaysUnobserved(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(fmt.Sprintf("invalid=%t", invalid), func(t *testing.T) {
			recorder := new(trajectory.Recorder)
			model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
				return &chat.Response{Output: &chat.Output{FinishReason: chat.FinishReasonStop}}, nil
			})
			process, _, _ := startModelRecording(t, recorder, agent.DispatcherConfig{
				Model: model, Observer: incompleteModelObserver{recorder: recorder, invalid: invalid},
			}, run.NewMemoryStore(), false)
			recorded, err := recorder.Take(t.Context(), process, nil)
			if err != nil {
				t.Fatal(err)
			}
			calls := recorded.ModelCalls()
			if len(calls) != 1 || calls[0].Outcome != agent.ModelOutcomeUnobserved || calls[0].Response != nil || calls[0].Failure != "" {
				t.Fatalf("missing model settlement became observed evidence: %+v", calls)
			}
			var dropped uint64
			if invalid {
				dropped = 1
			}
			if gaps := recorded.Gaps(); gaps.UnpairedCalls != 1 || gaps.DroppedCallObservations != dropped {
				t.Fatalf("model evidence gaps=%+v", gaps)
			}
			if _, err := recorded.TotalTokens(); !errors.Is(err, trajectory.ErrIncompleteRecording) {
				t.Fatalf("unobserved call proved token usage: %v", err)
			}
		})
	}
}

func TestFailedModelAttemptsRetainRequestAndUnknownSettlement(t *testing.T) {
	for _, mode := range []string{"error", "nil", "invalid", "panic", "canceled", "stream_error"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				recorder := &trajectory.Recorder{}
				started := make(chan struct{})
				config := agent.DispatcherConfig{Model: chat.ModelFunc(func(ctx context.Context, _ *chat.Request) (*chat.Response, error) {
					close(started)
					switch mode {
					case "nil":
						return nil, nil
					case "invalid":
						return &chat.Response{}, nil
					case "panic":
						panic("provider panicked")
					case "canceled":
						<-ctx.Done()
						return nil, ctx.Err()
					default:
						return nil, errors.New("provider outcome unavailable")
					}
				})}
				if mode == "stream_error" {
					config.Model = nil
					config.Streamer = chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
						return func(yield func(*chat.ResponseDelta, error) bool) {
							close(started)
							yield(nil, errors.New("stream failed"))
						}
					})
				}
				process, _, _ := startModelRecording(t, recorder, config, run.NewMemoryStore(), false)
				<-started
				if mode == "canceled" {
					if err := process.Cancel(t.Context(), "stop provider call"); err != nil {
						t.Fatal(err)
					}
				} else {
					synctest.Wait()
					if err := process.Kill(t.Context(), "export uncertain provider attempt"); err != nil {
						t.Fatal(err)
					}
				}
				recorded, err := recorder.Take(t.Context(), process, nil)
				if err != nil {
					t.Fatal(err)
				}
				calls := recorded.ModelCalls()
				if len(calls) != 1 || calls[0].Outcome != agent.ModelOutcomeUnknown || calls[0].Response != nil || calls[0].Failure == "" || calls[0].Request.Messages[0].Parts[0].Text != "retained question" {
					t.Fatalf("failed model evidence = %+v", calls)
				}
				call := calls[0]
				startedMatch, settledMatch := false, false
				for _, event := range recorded.Events() {
					if id, ok := event.EffectID(); !ok || id != call.EffectID {
						continue
					}
					if fact, ok := event.EffectStarted(); ok {
						startedMatch = fact.AttemptID() == call.AttemptID
					}
					if fact, ok := event.EffectFinished(); ok {
						settledMatch = fact.AttemptID() == call.AttemptID && fact.SettlementStatus() == run.SettlementStatusUnknown
					}
				}
				if !startedMatch || !settledMatch {
					t.Fatal("model observation lost its exact physical attempt")
				}
				recordConfig := trajectoryConfig(recorded)
				recordConfig.Coverage = interactionCoverage(recordConfig.Events)
				recorded, err = trajectory.New(recordConfig)
				if err != nil {
					t.Fatal(err)
				}
				if _, accountingErr := recorded.TotalTokens(); !errors.Is(accountingErr, trajectory.ErrIncompleteRecording) {
					t.Fatalf("unknown model proved token cost: %v", accountingErr)
				}
				encoded, err := jsonv2.Marshal(recorded)
				if err != nil {
					t.Fatal(err)
				}
				var restored trajectory.Trajectory
				if err := jsonv2.Unmarshal(encoded, &restored); err != nil {
					t.Fatal(err)
				}
				if restored.ModelCalls()[0].AttemptID != call.AttemptID || restored.ModelCalls()[0].Request == nil {
					t.Fatal("portable evidence lost request or attempt identity")
				}
			})
		})
	}
}

type failingModelSettlementCommitter struct{ *run.MemoryStore }

func (f failingModelSettlementCommitter) CommitEffect(ctx context.Context, boundary run.EffectBoundary) error {
	if boundary.Kind() == run.EffectBoundaryKindSettled && boundary.Request().Effect().Target() == run.EffectTargetDispatcher {
		return errors.New("settlement persistence unavailable")
	}
	return f.MemoryStore.CommitEffect(ctx, boundary)
}

func TestRecorderExportsRuntimeStoppedWithoutInventingRootResult(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		recorder := &trajectory.Recorder{}
		model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
			message := chat.NewAssistantMessage(chat.NewTextPart("observed response"))
			return &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}}, nil
		})
		process, _, _ := startModelRecording(t, recorder, agent.DispatcherConfig{Model: model}, failingModelSettlementCommitter{run.NewMemoryStore()}, false)
		if _, err := process.Await(t.Context()); err == nil {
			t.Fatal("fixture did not stop the runtime")
		}
		recorded, err := recorder.Take(t.Context(), process, nil)
		if err != nil {
			t.Fatalf("runtime stopped evidence was not exportable: %v", err)
		}
		if recorded.Termination().Valid() || recorded.HistoryComplete() {
			t.Fatal("runtime failure invented a committed root result")
		}
		if calls := recorded.ModelCalls(); len(calls) != 1 || calls[0].Outcome != agent.ModelOutcomeSucceeded || calls[0].Response.Text() != "observed response" {
			t.Fatalf("observed response was retracted: %+v", calls)
		}
		stopped := false
		for _, event := range recorded.Events() {
			stopped = stopped || event.Name() == run.EventRuntimeStopped
		}
		if !stopped {
			t.Fatal("runtime stop fact was lost")
		}
		data, err := jsonv2.Marshal(recorded)
		if err != nil {
			t.Fatal(err)
		}
		var decoded trajectory.Trajectory
		if err := jsonv2.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		if _, err := (trajectory.Evaluator{}).Evaluate(t.Context(), trajectory.Sample{Actual: decoded, Expected: trajectory.Expectation{Status: run.StatusCompleted}}); !errors.Is(err, trajectory.ErrIncompleteRecording) {
			t.Fatalf("unknown root result became an eval failure: %v", err)
		}
	})
}

// This fixture authorizes replay only for its deterministic in-memory model.
type replayableTestModelDispatcher struct{ *agent.Dispatcher }

func (replayableTestModelDispatcher) Policy(run.Effect) run.EffectPolicy {
	return run.EffectPolicy{Replay: run.ReplayPolicySameIdentity}
}

func TestRecorderKeepsEveryPhysicalModelAttemptForOneLogicalEffect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		recorder := &trajectory.Recorder{}
		invocations := make(chan agent.ModelInvocation, 2)
		count := 0
		model := chat.ModelFunc(func(ctx context.Context, _ *chat.Request) (*chat.Response, error) {
			invocation, _ := agent.ModelInvocationFromContext(ctx)
			invocations <- invocation
			count++
			if count == 1 {
				return nil, errors.New("first physical attempt was unknown")
			}
			message := chat.NewAssistantMessage(chat.NewTextPart("resolved on replay"))
			return &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}}, nil
		})
		process, _, _ := startModelRecording(t, recorder, agent.DispatcherConfig{Model: model}, run.NewMemoryStore(), true)
		synctest.Wait()
		first := <-invocations
		if err := process.ReplayUnknownEffect(t.Context(), first.EffectID()); err != nil {
			t.Fatal(err)
		}
		second := <-invocations
		firstID, firstBound := first.AttemptID()
		secondID, secondBound := second.AttemptID()
		if !firstBound || !secondBound || firstID == secondID || first.EffectID() != second.EffectID() {
			t.Fatal("logical identity and physical attempts were conflated")
		}
		recorded, err := recorder.Take(t.Context(), process, nil)
		if err != nil {
			t.Fatal(err)
		}
		config := trajectoryConfig(recorded)
		config.Coverage = interactionCoverage(config.Events)
		recorded, err = trajectory.New(config)
		if err != nil {
			t.Fatal(err)
		}
		if len(recorded.ModelCalls()) != 2 || len(config.Coverage.Models) != 1 {
			t.Fatal("replay lost a model attempt or duplicated logical coverage")
		}
		if _, digestErr := recorded.BehaviorDigest(rawOutputProjection); digestErr != nil {
			t.Fatalf("two attempts did not satisfy physical coverage: %v", digestErr)
		}
		for _, call := range recorded.ModelCalls() {
			if call.AttemptID == firstID && call.Outcome != agent.ModelOutcomeUnknown || call.AttemptID == secondID && call.Outcome != agent.ModelOutcomeSucceeded {
				t.Fatal("replay overwrote the earlier Unknown")
			}
		}
		config.ModelCalls = config.ModelCalls[:1]
		missing, err := trajectory.New(config)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := missing.BehaviorDigest(rawOutputProjection); !errors.Is(err, trajectory.ErrIncompleteRecording) {
			t.Fatalf("one call falsely covered two physical attempts: %v", err)
		}
	})
}

func TestRecorderPreservesUnknownAttemptAndLaterInvestigatedResolution(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		recorder := &trajectory.Recorder{}
		model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
			return nil, errors.New("response acknowledgment lost")
		})
		process, engine, deployment := startModelRecording(t, recorder, agent.DispatcherConfig{Model: model}, run.NewMemoryStore(), false)
		synctest.Wait()
		snapshot, err := engine.CaptureTree(t.Context(), process.Relation().ProcessID())
		if err != nil {
			t.Fatal(err)
		}
		var effect run.EffectID
		for _, state := range snapshot.ProcessSnapshots() {
			if state.Relation().ProcessID() == process.Relation().ProcessID() {
				effect = state.UnknownEffectIDs()[0]
			}
		}
		request, ok := snapshot.EffectRequest(process.Relation().ProcessID(), effect)
		if !ok {
			t.Fatal("investigation has no retained logical request")
		}
		if _, physical := request.AttemptID(); physical {
			t.Fatal("snapshot invented a physical attempt")
		}
		message := chat.NewAssistantMessage(chat.NewTextPart("investigated response"))
		response := &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}}
		settlement, err := agent.SettleModelResult(deployment, request, response)
		if err != nil {
			t.Fatal(err)
		}
		if resolveErr := process.ResolveUnknownEffect(t.Context(), effect, settlement); resolveErr != nil {
			t.Fatal(resolveErr)
		}
		recorded, err := recorder.Take(t.Context(), process, nil)
		if err != nil {
			t.Fatal(err)
		}
		if calls := recorded.ModelCalls(); len(calls) != 1 || calls[0].Outcome != agent.ModelOutcomeUnknown {
			t.Fatalf("investigation rewrote physical history: %+v", calls)
		}
		resolved := false
		for _, event := range recorded.Events() {
			if fact, ok := event.EffectResolved(); ok {
				id, _ := event.EffectID()
				resolved = event.Relation().ProcessID() == process.Relation().ProcessID() && id == effect && fact.SettlementStatus() == run.SettlementStatusSucceeded
			}
		}
		if !resolved || recorded.Termination().Status() != run.StatusCompleted {
			t.Fatal("resolution chain lost committed evidence")
		}
	})
}

func TestTakeAccountsForQueuedAndLostEventsWithoutLosingEarlyModelCalls(t *testing.T) {
	for _, size := range []int{1, 128} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			recorder := &trajectory.Recorder{}
			release := make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			delayed := run.EventListenerFunc(func(ctx context.Context, e run.Event) { <-release; recorder.OnEvent(ctx, e) })
			config := agent.DispatcherConfig{Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
				return &chat.Response{Output: &chat.Output{Message: new(chat.NewAssistantMessage(chat.NewTextPart("done"))), FinishReason: chat.FinishReasonStop}}, nil
			})}
			process, _, _ := startModelRecordingWithListener(t, recorder, config, run.NewMemoryStore(), false, delayed, size)
			if err := process.Join(t.Context()); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Millisecond)
			defer cancel()
			if _, err := recorder.Take(ctx, process, nil); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("early take: %v", err)
			}
			// A host can abandon the export wait without authorizing evidence deletion.
			discardCtx, stop := context.WithTimeout(t.Context(), 5*time.Millisecond)
			if err := recorder.Discard(discardCtx, process); !errors.Is(err, context.DeadlineExceeded) {
				stop()
				t.Fatalf("discard bypassed a still-running collector: %v", err)
			}
			stop()
			unblock()
			captured, err := recorder.Take(t.Context(), process, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(captured.ModelCalls()) != 1 || captured.ModelCalls()[0].Outcome != agent.ModelOutcomeSucceeded {
				t.Fatalf("early model evidence: %v", captured.ModelCalls())
			}
			published, err := process.FlushObservations(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if captured.Gaps().DroppedEvents != published-uint64(len(captured.Events())) || (captured.Gaps().DroppedEvents > 0) != (size == 1) {
				t.Fatalf("published=%d recorded=%d gaps=%v", published, len(captured.Events()), captured.Gaps())
			}
		})
	}
}
