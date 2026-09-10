package agent

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestCaptureTreeAllowsTerminationWhileEffectsDrain(t *testing.T) {
	for _, control := range []string{"host cancellation", "host deadline", "request cancellation", "kill"} {
		t.Run(control, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				dispatcher := &cancellationDispatcher{
					entered: make(chan EffectRequest, 1), canceled: make(chan struct{}),
					release: make(chan struct{}), status: SettlementStatusSucceeded,
				}
				release := sync.OnceFunc(func() { close(dispatcher.release) })
				defer release()
				engine, err := NewEngine(EngineConfig{})
				if err != nil {
					t.Fatal(err)
				}
				hostContext, cancelHost := context.WithTimeout(t.Context(), time.Hour)
				defer cancelHost()
				deployment := engineTestDeployment(t, newEngineTestDefinition(t, "engine.effect", "effect"), dispatcher)
				input, err := EncodeInput(engineTestInput{Value: "capture cancellation"})
				if err != nil {
					t.Fatal(err)
				}
				process, err := engine.Start(hostContext, deployment, input)
				if err != nil {
					t.Fatal(err)
				}
				request := <-dispatcher.entered
				captureContext, cancelCapture := context.WithCancel(t.Context())
				defer cancelCapture()
				captured := make(chan treeFreezeAcquisitionResult, 1)
				go func() {
					snapshot, captureErr := engine.CaptureTree(captureContext, process.ID())
					captured <- treeFreezeAcquisitionResult{snapshot: snapshot, err: captureErr}
				}()
				synctest.Wait()
				inspection := requireTreeInspection(t, engine, process.ID())
				if inspection.Freeze != TreeFreezeAcquiring {
					t.Fatalf("capture did not wait for the active Effect: %+v", inspection)
				}
				controlled := make(chan error, 1)
				go func() {
					switch control {
					case "host cancellation":
						cancelHost()
						controlled <- nil
					case "host deadline":
						controlled <- nil
					case "request cancellation":
						controlled <- process.RequestCancellation(t.Context(), "cancel while capturing")
					case "kill":
						controlled <- process.Kill(t.Context(), "kill while capturing")
					}
				}()
				if control == "host deadline" {
					time.Sleep(time.Hour)
				}
				synctest.Wait()
				canceled := false
				select {
				case <-dispatcher.canceled:
					canceled = true
				default:
					t.Error("capture blocked the termination needed to drain its active Effect")
					cancelCapture()
				}
				if canceled {
					select {
					case result := <-captured:
						t.Fatalf("capture crossed an unsettled Effect after cancellation: %+v", result)
					default:
					}
				}
				release()
				capture := <-captured
				if controlErr := <-controlled; controlErr != nil {
					t.Fatal(controlErr)
				}
				result := mustAwait(t, process)
				wantStatus, wantCause := StatusCanceled, TerminationCauseHostCancellation
				switch control {
				case "host deadline":
					wantStatus, wantCause = StatusTimedOut, TerminationCauseHostDeadline
				case "kill":
					wantStatus, wantCause = StatusKilled, TerminationCauseEngineKill
				}
				if result.Status() != wantStatus || result.Termination().Cause() != wantCause {
					t.Errorf("termination changed: %+v", result.Termination())
				}
				if joinErr := process.Join(t.Context()); joinErr != nil {
					t.Fatal(joinErr)
				}
				mustCloseEngine(t, engine)
				if !canceled {
					return
				}
				if capture.err != nil || !capture.snapshot.Valid() {
					t.Fatalf("capture did not complete after the Effect settled: %v", capture.err)
				}
				wire, err := capture.snapshot.ProcessSnapshots()[0].wire()
				if err != nil || wire.Prepared == nil || len(wire.Prepared.Effects) != 1 ||
					wire.Prepared.Effects[0].ID != request.ID() || !wire.Prepared.Effects[0].definitelySettled() {
					t.Fatalf("capture lost the settled Effect: %+v, %v", wire.Prepared, err)
				}
				restoredEngine, err := NewEngine(EngineConfig{})
				if err != nil {
					t.Fatal(err)
				}
				restored, err := restoredEngine.RestoreTree(t.Context(), deployment, capture.snapshot)
				if err != nil {
					t.Fatal(err)
				}
				restoredResult := mustAwait(t, restored)
				if restoredResult.Status() != wantStatus || restoredResult.Termination().Cause() != wantCause ||
					restoredResult.Usage() != result.Usage() || len(dispatcher.entered) != 0 {
					t.Fatal("restoration lost termination intent, changed usage, or repeated the Effect")
				}
				if joinErr := restored.Join(t.Context()); joinErr != nil {
					t.Fatal(joinErr)
				}
				mustCloseEngine(t, restoredEngine)
			})
		})
	}
}
