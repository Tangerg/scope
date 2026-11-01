package coordination_test

import (
	startrand "crypto/rand"

	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Tangerg/scope/agent/coordination"
	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/run/runtest"
)

func TestTimerRejectsNilContextBeforeProtocolValidation(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("nil context became a protocol result")
		}
	}()
	var nilContext context.Context
	_, _ = (coordination.DeadlineDispatcher{}).Dispatch(nilContext, run.EffectRequest{}, nil)
}

func TestDeadlineRestoresTheSameAbsoluteTimerAndEffectIdentity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := time.Now()
		deadline := started.Add(10 * time.Second)
		dispatcher := &recordingTimer{}
		deployment := deadlineBinding(t, dispatcher)
		store := run.NewMemoryStore()
		engine, err := run.New(t.Context(), run.Config{Store: store})
		if err != nil {
			t.Fatal(err)
		}
		process, err := engine.Start(t.Context(), deployment, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: encodedInput(t, deadline)})
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		pending, found, loadErr := store.LoadTree(t.Context(), process.Relation().ProcessID())
		if loadErr != nil || !found || len(dispatcher.identities()) != 1 {
			t.Fatalf("pending timer exists=%t calls=%d error=%v", found, len(dispatcher.identities()), loadErr)
		}
		time.Sleep(4 * time.Second)
		restoredEngine, err := run.New(t.Context(), run.Config{Store: store})
		if err != nil {
			t.Fatal(err)
		}
		restored, err := restoredEngine.Recover(t.Context(), deployment, pending)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		identities := dispatcher.identities()
		if len(identities) != 2 || identities[0] != identities[1] {
			t.Fatalf("timer replay changed identity: %v", identities)
		}
		if killErr := process.Kill(t.Context(), "retire previous timer writer"); killErr != nil {
			t.Fatal(killErr)
		}
		if _, staleErr := process.Await(t.Context()); !errors.Is(staleErr, run.ErrTreeIncarnationConflict) {
			t.Fatalf("retired timer writer = %v", staleErr)
		}
		if got := completedOutput[time.Time](t, restored); !got.Equal(deadline) || !time.Now().Equal(deadline) {
			t.Fatalf("restored timer reached %v at %v, want original deadline %v", got, time.Now(), deadline)
		}
		if usage := result(t, restored).Usage(); usage != (run.Usage{CommittedSteps: 2, PreparedEffects: 1, AcceptedSignals: 1}) {
			t.Fatalf("timer replay charged work again: %+v", usage)
		}
		if joinErr := restored.Join(t.Context()); joinErr != nil {
			t.Fatal(joinErr)
		}
		closeEngine(t, restoredEngine)
		closeEngine(t, engine)
	})
}

func TestDeadlineCancellationStopsOwnedTimerWithoutAdvancingTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := time.Now()
		dispatcher := &recordingTimer{}
		deployment := deadlineBinding(t, dispatcher)
		engine, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore()})
		if err != nil {
			t.Fatal(err)
		}
		process, err := engine.Start(t.Context(), deployment, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: encodedInput(t, started.Add(time.Hour))})
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if len(dispatcher.identities()) != 1 {
			t.Fatal("timer has not entered its owned call")
		}
		if cancelErr := process.Cancel(t.Context(), "replace deadline"); cancelErr != nil {
			t.Fatal(cancelErr)
		}
		if joinErr := process.Join(t.Context()); joinErr != nil {
			t.Fatal(joinErr)
		}
		termination := result(t, process).Termination()
		if termination.Status() != run.StatusCanceled || len(termination.UnresolvedEffectIDs()) != 0 || !time.Now().Equal(started) {
			t.Fatalf("timer cancellation = %+v at %v", termination, time.Now())
		}
		closeEngine(t, engine)
	})
}

func TestDeadlineAcceptsPastInstantAndRejectsZero(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deployment := deadlineBinding(t, coordination.DeadlineDispatcher{})
		if _, err := deployment.Definition().Start(encodedInput(t, time.Time{})); !errors.Is(err, run.ErrInvalidPayload) {
			t.Fatalf("zero deadline = %v", err)
		}
		engine, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore()})
		if err != nil {
			t.Fatal(err)
		}
		past := time.Now().Add(-time.Hour)
		process, err := engine.Start(t.Context(), deployment, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: encodedInput(t, past)})
		if err != nil {
			t.Fatal(err)
		}
		if got := completedOutput[time.Time](t, process); !got.Equal(past) {
			t.Fatalf("expired deadline changed to %v", got)
		}
		closeEngine(t, engine)
	})
}

func TestDeadlineDefinitionConformance(t *testing.T) {
	deployment := deadlineBinding(t, coordination.DeadlineDispatcher{})
	definition := deployment.Definition()
	input := encodedInput(t, time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC))
	execution, err := definition.Start(input)
	if err != nil {
		t.Fatal(err)
	}
	state, err := execution.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	config := runtest.DefinitionConformanceConfig{
		Definition: definition, Input: input,
		RejectedCases: []runtest.RejectedStepConformanceCase{{
			Name:        "unsolicited Signal before the timer",
			State:       state,
			Signals:     []run.Signal{externalSignal(t, "", "unsolicited")},
			FailureKind: run.FailureKindContract,
			FailureCode: "coordination.protocol.invalid",
		}},
	}
	runtest.RunDefinitionConformance(t, config)
	conformancetest.CheckRestoreCancellation(t, config.Definition, state)
}

type recordingTimer struct {
	mu       sync.Mutex
	requests []run.EffectID
}

func (r *recordingTimer) Policy(effect run.Effect) run.EffectPolicy {
	return (coordination.DeadlineDispatcher{}).Policy(effect)
}

func (r *recordingTimer) Dispatch(ctx context.Context, request run.EffectRequest, emit run.DeltaEmitter) (run.Settlement, error) {
	r.mu.Lock()
	r.requests = append(r.requests, request.ID())
	r.mu.Unlock()
	return (coordination.DeadlineDispatcher{}).Dispatch(ctx, request, emit)
}

func (r *recordingTimer) identities() []run.EffectID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]run.EffectID{}, r.requests...)
}

func TestDeadlineClassifiesInvalidSettlementThroughEngine(t *testing.T) {
	for _, payload := range []string{
		`{"reached":"yes"}`,
		`{"deadline":"2026-09-09T12:00:00Z","reached":true}`,
	} {
		dispatcher, err := runtest.NewScriptedDispatcher(runtest.ScriptedDispatcherConfig{ReplayPolicy: run.ReplayPolicyNever, Calls: []runtest.ScriptedCall{{SettlementStatus: run.SettlementStatusSucceeded, SettlementPayload: []byte(payload)}}})
		if err != nil {
			t.Fatal(err)
		}
		deployment := deadlineBinding(t, dispatcher)
		engine, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore()})
		if err != nil {
			t.Fatal(err)
		}
		process, err := engine.Start(t.Context(), deployment, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: encodedInput(t, time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))})
		if err != nil {
			t.Fatal(err)
		}
		final := result(t, process)
		failure, failed := final.Termination().Failure()
		if final.Termination().Status() != run.StatusFailed || !failed || failure.Kind() != run.FailureKindContract || failure.Code() != "coordination.protocol.invalid" || final.Termination().Cause() != run.TerminationCauseContractFailure {
			t.Fatalf("protocol failure = %+v, termination = %+v", failure, final.Termination())
		}
		closeEngine(t, engine)
	}
}

func TestDeadlineSettlementStatusOwnsWhetherItWasReached(t *testing.T) {
	for _, test := range []struct {
		status  run.SettlementStatus
		payload string
		want    run.Status
	}{
		{run.SettlementStatusSucceeded, `{}`, run.StatusCompleted},
		{run.SettlementStatusFailed, `"timer returned before its deadline"`, run.StatusFailed},
	} {
		dispatcher, err := runtest.NewScriptedDispatcher(runtest.ScriptedDispatcherConfig{ReplayPolicy: run.ReplayPolicyNever, Calls: []runtest.ScriptedCall{{SettlementStatus: test.status, SettlementPayload: []byte(test.payload)}}})
		if err != nil {
			t.Fatal(err)
		}
		engine, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore()})
		if err != nil {
			t.Fatal(err)
		}
		process, err := engine.Start(t.Context(), deadlineBinding(t, dispatcher), run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: encodedInput(t, time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))})
		if err != nil {
			t.Fatal(err)
		}
		final := result(t, process)
		failure, _ := final.Termination().Failure()
		if final.Termination().Status() != test.want || test.want == run.StatusFailed && failure.Code() != "coordination.deadline.interrupted" {
			t.Fatalf("%s settlement ended %s with %+v", test.status, final.Termination().Status(), failure)
		}
		closeEngine(t, engine)
	}
}

func TestDeadlineFailureSettlementPreservesAndValidatesDiagnostic(t *testing.T) {
	for _, resolved := range []bool{false, true} {
		for _, test := range []struct {
			payload string
			valid   bool
		}{
			{`"timer source lost its clock"`, true}, {`{}`, false}, {`null`, false}, {`""`, false}, {`" untrimmed"`, false},
		} {
			t.Run(fmt.Sprintf("resolved=%t/%s", resolved, test.payload), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					status, payload := run.SettlementStatusFailed, []byte(test.payload)
					if resolved {
						status, payload = run.SettlementStatusUnknown, []byte(`{}`)
					}
					dispatcher, err := runtest.NewScriptedDispatcher(runtest.ScriptedDispatcherConfig{ReplayPolicy: run.ReplayPolicyNever, Calls: []runtest.ScriptedCall{{SettlementStatus: status, SettlementPayload: payload}}})
					if err != nil {
						t.Fatal(err)
					}
					engine, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore()})
					if err != nil {
						t.Fatal(err)
					}
					process, err := engine.Start(t.Context(), deadlineBinding(t, dispatcher), run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: encodedInput(t, time.Now().Add(time.Hour))})
					if err != nil {
						t.Fatal(err)
					}
					if resolved {
						synctest.Wait()
						inspection, err := engine.InspectTree(t.Context(), process.Relation().ProcessID())
						if err != nil {
							t.Fatal(err)
						}
						ids := inspection.Processes[0].Snapshot.UnknownEffectIDs()
						if len(ids) != 1 {
							t.Fatalf("unknown timer=%v", ids)
						}
						settlement, err := run.NewSettlement(run.SettlementStatusFailed, []byte(test.payload))
						if err != nil {
							t.Fatal(err)
						}
						if err := process.ResolveUnknownEffect(t.Context(), ids[0], settlement); err != nil {
							t.Fatal(err)
						}
					}
					failure, ok := result(t, process).Termination().Failure()
					if !ok {
						t.Fatal("failed timer completed")
					}
					if test.valid {
						if failure.Kind() != run.FailureKindExternal || failure.Message() != "timer source lost its clock" {
							t.Fatalf("failure diagnostic=%+v", failure)
						}
					} else if failure.Kind() != run.FailureKindContract || failure.Code() != "coordination.protocol.invalid" {
						t.Fatalf("malformed diagnostic=%+v", failure)
					}
					closeEngine(t, engine)
				})
			})
		}
	}
}
