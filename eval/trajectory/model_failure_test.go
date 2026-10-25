package trajectory_test

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"iter"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
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
			config := interaction.DispatcherConfig{}
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
			process, _, _ := startModelRecording(t, recorder, config, agent.NewMemoryTreeCommitter(), false)
			result, err := process.Await(t.Context())
			if err != nil || result.Termination().Status() != agent.StatusFailed {
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
			if len(observed) != 1 || observed[0].Outcome != interaction.ModelOutcomeFailed || observed[0].Response.Text() != "completed generation" || observed[0].Failure == "" {
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
			if tokens, err := restored.TotalTokens(); err != nil || tokens != 28 || restored.ModelCalls()[0].Outcome != interaction.ModelOutcomeFailed || calls.Load() != 1 {
				t.Fatalf("restored usage=%d error=%v model calls=%d", tokens, err, calls.Load())
			}
		})
	}
}

func startModelRecording(t *testing.T, recorder *trajectory.Recorder, config interaction.DispatcherConfig, committer agent.TreeCommitter, replay bool) (*agent.Process, *agent.Engine, *interaction.Dispatcher) {
	t.Helper()
	definition, err := interaction.NewDefinition(interaction.DefinitionConfig{Name: "test.model_recording", Description: "Record actual model boundaries.", MaxModelCalls: agent.NewQuota(1)})
	if err != nil {
		t.Fatal(err)
	}
	if config.Observer == nil {
		config.Observer = recorder
	}
	dispatcher, err := interaction.NewDispatcher(definition, config)
	if err != nil {
		t.Fatal(err)
	}
	var external agent.Dispatcher = dispatcher
	if replay {
		external = replayableTestModelDispatcher{dispatcher}
	}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{Definition: definition, Dispatcher: external,
		ImplementationDigest: agent.ComputeDigest([]byte("model-recording-implementation")), ConfigurationDigest: agent.ComputeDigest([]byte("model-recording-configuration"))})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: committer, EventListeners: []agent.EventListener{recorder}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	input, err := agent.EncodePayload(interaction.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("retained question"))}})
	if err != nil {
		t.Fatal(err)
	}
	process, err := engine.Start(t.Context(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	return process, engine, dispatcher
}

func TestDefiniteModelOutcomesPreserveClassificationAndTokenEvidence(t *testing.T) {
	for _, test := range []struct {
		name       string
		cause      error
		outcome    interaction.ModelOutcome
		knownUsage bool
	}{
		{name: "not executed", cause: &chat.CallFailure{Kind: chat.CallFailureNotExecuted, Cause: errors.New("input rejected")}, outcome: interaction.ModelOutcomeNotExecuted, knownUsage: true},
		{name: "failed after dispatch", cause: &chat.CallFailure{Kind: chat.CallFailureFailed, Cause: errors.New("output rejected")}, outcome: interaction.ModelOutcomeFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := new(trajectory.Recorder)
			cause := fmt.Errorf("model rejected request: %w", test.cause)
			model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) { return nil, cause })
			process, engine, _ := startModelRecording(t, recorder, interaction.DispatcherConfig{Model: model}, agent.NewMemoryTreeCommitter(), false)
			result, err := process.Await(t.Context())
			if err != nil || result.Termination().Status() != agent.StatusFailed {
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
					finished = fact.SettlementStatus() == agent.SettlementStatusFailed
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

func (i incompleteModelObserver) OnModelStarted(ctx context.Context, invocation interaction.ModelInvocation, request *chat.Request) {
	i.recorder.OnModelStarted(ctx, invocation, request)
}

func (i incompleteModelObserver) OnModelSettled(ctx context.Context, invocation interaction.ModelInvocation, _ interaction.ModelSettlement) {
	if i.invalid {
		i.recorder.OnModelSettled(ctx, invocation, interaction.ModelSettlement{Outcome: interaction.ModelOutcomeUnobserved})
	}
}

func TestMissingOrInvalidModelObservationStaysUnobserved(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(fmt.Sprintf("invalid=%t", invalid), func(t *testing.T) {
			recorder := new(trajectory.Recorder)
			model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
				return &chat.Response{Output: &chat.Output{FinishReason: chat.FinishReasonStop}}, nil
			})
			process, _, _ := startModelRecording(t, recorder, interaction.DispatcherConfig{
				Model: model, Observer: incompleteModelObserver{recorder: recorder, invalid: invalid},
			}, agent.NewMemoryTreeCommitter(), false)
			recorded, err := recorder.Take(t.Context(), process, nil)
			if err != nil {
				t.Fatal(err)
			}
			calls := recorded.ModelCalls()
			if len(calls) != 1 || calls[0].Outcome != interaction.ModelOutcomeUnobserved || calls[0].Response != nil || calls[0].Failure != "" {
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
				config := interaction.DispatcherConfig{Model: chat.ModelFunc(func(ctx context.Context, _ *chat.Request) (*chat.Response, error) {
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
				process, _, _ := startModelRecording(t, recorder, config, agent.NewMemoryTreeCommitter(), false)
				<-started
				if mode == "canceled" {
					if err := process.RequestCancellation(t.Context(), "stop provider call"); err != nil {
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
				if len(calls) != 1 || calls[0].Outcome != interaction.ModelOutcomeUnknown || calls[0].Response != nil || calls[0].Failure == "" || calls[0].Request.Messages[0].Parts[0].Text != "retained question" {
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
						settledMatch = fact.AttemptID() == call.AttemptID && fact.SettlementStatus() == agent.SettlementStatusUnknown
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

type failingModelSettlementCommitter struct{ *agent.MemoryTreeCommitter }

func (f failingModelSettlementCommitter) CommitEffect(ctx context.Context, boundary agent.EffectBoundary) error {
	if boundary.Kind() == agent.EffectBoundaryKindSettled && boundary.Request().Effect().Target() == agent.EffectTargetDispatcher {
		return errors.New("settlement persistence unavailable")
	}
	return f.MemoryTreeCommitter.CommitEffect(ctx, boundary)
}

func TestRecorderExportsRuntimeStoppedWithoutInventingRootResult(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		recorder := &trajectory.Recorder{}
		model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
			message := chat.NewAssistantMessage(chat.NewTextPart("observed response"))
			return &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}}, nil
		})
		process, _, _ := startModelRecording(t, recorder, interaction.DispatcherConfig{Model: model}, failingModelSettlementCommitter{agent.NewMemoryTreeCommitter()}, false)
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
		if calls := recorded.ModelCalls(); len(calls) != 1 || calls[0].Outcome != interaction.ModelOutcomeSucceeded || calls[0].Response.Text() != "observed response" {
			t.Fatalf("observed response was retracted: %+v", calls)
		}
		stopped := false
		for _, event := range recorded.Events() {
			stopped = stopped || event.Name() == agent.EventRuntimeStopped
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
		if _, err := (trajectory.Evaluator{}).Evaluate(t.Context(), trajectory.Sample{Actual: decoded, Expected: trajectory.Expectation{Status: agent.StatusCompleted}}); !errors.Is(err, trajectory.ErrIncompleteRecording) {
			t.Fatalf("unknown root result became an eval failure: %v", err)
		}
	})
}

// This fixture authorizes replay only for its deterministic in-memory model.
type replayableTestModelDispatcher struct{ *interaction.Dispatcher }

func (replayableTestModelDispatcher) Policy(agent.Effect) agent.EffectPolicy {
	return agent.EffectPolicy{Replay: agent.ReplayPolicySameIdentity}
}

func TestRecorderKeepsEveryPhysicalModelAttemptForOneLogicalEffect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		recorder := &trajectory.Recorder{}
		invocations := make(chan interaction.ModelInvocation, 2)
		count := 0
		model := chat.ModelFunc(func(ctx context.Context, _ *chat.Request) (*chat.Response, error) {
			invocation, _ := interaction.ModelInvocationFromContext(ctx)
			invocations <- invocation
			count++
			if count == 1 {
				return nil, errors.New("first physical attempt was unknown")
			}
			message := chat.NewAssistantMessage(chat.NewTextPart("resolved on replay"))
			return &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}}, nil
		})
		process, _, _ := startModelRecording(t, recorder, interaction.DispatcherConfig{Model: model}, agent.NewMemoryTreeCommitter(), true)
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
			if call.AttemptID == firstID && call.Outcome != interaction.ModelOutcomeUnknown || call.AttemptID == secondID && call.Outcome != interaction.ModelOutcomeSucceeded {
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
		process, engine, dispatcher := startModelRecording(t, recorder, interaction.DispatcherConfig{Model: model}, agent.NewMemoryTreeCommitter(), false)
		synctest.Wait()
		snapshot, err := engine.CaptureTree(t.Context(), process.Relation().ProcessID())
		if err != nil {
			t.Fatal(err)
		}
		var effect agent.EffectID
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
		settlement, err := dispatcher.SettleModelResult(request, response, []chat.Message{chat.NewUserMessage(chat.NewTextPart("retained question"))})
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
		if calls := recorded.ModelCalls(); len(calls) != 1 || calls[0].Outcome != interaction.ModelOutcomeUnknown {
			t.Fatalf("investigation rewrote physical history: %+v", calls)
		}
		resolved := false
		for _, event := range recorded.Events() {
			if fact, ok := event.EffectResolved(); ok {
				id, _ := event.EffectID()
				resolved = event.Relation().ProcessID() == process.Relation().ProcessID() && id == effect && fact.SettlementStatus() == agent.SettlementStatusSucceeded
			}
		}
		if !resolved || recorded.Termination().Status() != agent.StatusCompleted {
			t.Fatal("resolution chain lost committed evidence")
		}
	})
}
