package agent

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
)

type blockedRestoreDefinition struct {
	Definition
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (b *blockedRestoreDefinition) Restore(state ExecutionState) (Execution, error) {
	var decoded treeRuntimeTestState
	if err := json.Unmarshal(state.Payload(), &decoded); err != nil {
		return nil, err
	}
	if decoded.Role == treeRuntimeRoleBlocked && b.armed.Swap(false) {
		close(b.entered)
		<-b.release
	}
	return b.Definition.Restore(state)
}

func TestStaleStepRestoreDoesNotBlockTreeOwner(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		original, probe := newTreeRuntimeTestDeployment(t)
		probe.fastStepRelease = make(chan struct{})
		releaseFast := sync.OnceFunc(func() { close(probe.fastStepRelease) })
		definition := &blockedRestoreDefinition{
			Definition: original.Definition(), entered: make(chan struct{}), release: make(chan struct{}),
		}
		releaseRestore := sync.OnceFunc(func() { close(definition.release) })
		reference := original.DeploymentRef()
		deployment, err := NewDeployment(DeploymentConfig{
			Definition: definition, Dispatcher: childTestDispatcher{},
			ImplementationDigest: reference.ImplementationDigest(), ConfigurationDigest: reference.ConfigurationDigest(),
		})
		if err != nil {
			t.Fatal(err)
		}
		engine, err := NewEngine(EngineConfig{})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { releaseFast(); releaseRestore(); mustCloseEngine(t, engine) }()
		input, err := EncodeInput(treeRuntimeTestInput{Role: treeRuntimeRoleRoot})
		if err != nil {
			t.Fatal(err)
		}
		root, err := engine.Start(t.Context(), deployment, input)
		if err != nil {
			t.Fatal(err)
		}
		<-probe.blockedStepStarted
		<-probe.fastStepReady
		blocked, ok := engine.Process(root.ID().effectID(1, 0).childProcessID())
		if !ok {
			t.Fatal("blocked child was not published")
		}
		fast, ok := engine.Process(root.ID().effectID(1, 1).childProcessID())
		if !ok {
			t.Fatal("fast child was not published")
		}
		definition.armed.Store(true)
		if err := blocked.Pause(t.Context(), "invalidate the active Step"); err != nil {
			t.Fatal(err)
		}
		<-definition.entered
		inspected := make(chan treeInspectionResponse, 1)
		go func() {
			inspection, inspectionErr := engine.InspectTree(t.Context(), root.ID())
			inspected <- treeInspectionResponse{inspection: inspection, err: inspectionErr}
		}()
		releaseFast()
		completed := make(chan Result, 1)
		go func() {
			result, awaitErr := fast.Await(t.Context())
			if awaitErr != nil {
				t.Error(awaitErr)
			}
			completed <- result
		}()
		synctest.Wait()
		select {
		case response := <-inspected:
			if response.err != nil {
				t.Fatal(response.err)
			}
			report, found := response.inspection.Process(blocked.ID())
			if !found || report.Work != ProcessWorkRestore {
				t.Errorf("restoring child inspection = %+v", report)
			}
		default:
			t.Error("stale Step restoration blocked tree inspection")
		}
		select {
		case result := <-completed:
			if result.Status() != StatusCompleted {
				t.Errorf("sibling status = %s", result.Status())
			}
		default:
			t.Error("stale Step restoration blocked sibling completion")
		}
		captured := make(chan treeFreezeAcquisitionResult, 1)
		go func() {
			snapshot, captureErr := engine.CaptureTree(t.Context(), root.ID())
			captured <- treeFreezeAcquisitionResult{snapshot: snapshot, err: captureErr}
		}()
		synctest.Wait()
		select {
		case capture := <-captured:
			if capture.err != nil || !capture.snapshot.Valid() {
				t.Fatalf("capture during pure restoration = %v", capture.err)
			}
		default:
			t.Error("pure restoration blocked capture of committed state")
		}
		controlled := make(chan error, 1)
		go func() { controlled <- blocked.Kill(t.Context(), "stop while restoration drains") }()
		synctest.Wait()
		select {
		case controlErr := <-controlled:
			if controlErr != nil {
				t.Fatal(controlErr)
			}
		default:
			t.Error("stale Step restoration blocked cancellation")
		}
		releaseRestore()
		if result := mustAwait(t, blocked); result.Status() != StatusKilled {
			t.Fatalf("restored stale result escaped cancellation: %s", result.Status())
		}
		if err := root.Kill(t.Context(), "test cleanup"); err != nil {
			t.Fatal(err)
		}
		_ = mustAwait(t, root)
	})
}
