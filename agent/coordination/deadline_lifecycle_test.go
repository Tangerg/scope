package coordination

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
)

type observedDeadlineDispatcher struct{ requests chan run.EffectRequest }

func (o observedDeadlineDispatcher) Policy(effect run.Effect) run.EffectPolicy {
	return (deadlineDispatcher{}).Policy(effect)
}

func (o observedDeadlineDispatcher) Dispatch(ctx context.Context, request run.EffectRequest, emit run.DeltaEmitter) (run.Settlement, error) {
	o.requests <- request
	return (deadlineDispatcher{}).Dispatch(ctx, request, emit)
}

func TestDeadlineWriterStopPreservesSameIdentityReplay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := run.NewMemoryStore()
		original := conformancetest.Runtime(t, run.Config{Store: store})
		requests := make(chan run.EffectRequest, 2)
		definition := require(newDeadline("test.deadline_restart", "Preserve the deadline across local writer shutdown."))
		program := require(run.Bind[time.Time, time.Time](original.Compiler(), run.BindingConfig{Definition: definition, Dispatcher: observedDeadlineDispatcher{requests: requests}, Configuration: Deadline{}}))
		deadline := time.Now().Add(time.Hour).UTC()
		handle := require(original.Start(t.Context(), program, run.StartRequest[time.Time]{ID: run.StartID{Scope: "test", Key: t.Name()}, Input: deadline}))
		first := <-requests
		if err := original.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
		replacement := conformancetest.Runtime(t, run.Config{Store: store})
		recovered := require(replacement.Recover(t.Context(), program, handle.ID()))
		inspection := require(recovered.Inspect(t.Context()))
		process, found := inspection.Process(handle.ID())
		if !found || len(process.PendingEffects) != 1 || process.PendingEffects[0].Outcome.Status() != run.SettlementStatusUnknown {
			t.Fatalf("local writer shutdown became a definite timer outcome: %+v", process)
		}
		replayed := make(chan error, 1)
		go func() { replayed <- recovered.ReplayUnknownEffect(t.Context(), first.ID()) }()
		second := <-requests
		if second.ID() != first.ID() || second.Epoch() <= first.Epoch() {
			t.Fatal("timer replay changed its effect identity or retained the former writer epoch")
		}
		synctest.Wait()
		if !time.Now().Before(deadline) {
			t.Fatal("timer replay did not remain pending before its deadline")
		}
		if err := <-replayed; err != nil {
			t.Fatal(err)
		}
		result := require(recovered.Await(t.Context()))
		output := require(result.Output())
		if !output.Equal(deadline) || !time.Now().Equal(deadline) {
			t.Fatalf("replayed timer completed at %v for deadline %v", time.Now(), output)
		}
		stopped, stop := context.WithCancelCause(t.Context())
		stop(run.ErrWriterStopped)
		outcome, err := (deadlineDispatcher{}).Dispatch(stopped, second, nil)
		if err != nil || outcome.Status() != run.SettlementStatusSucceeded {
			t.Fatalf("writer stop obscured an already reached deadline: status=%s error=%v", outcome.Status(), err)
		}
	})
}
