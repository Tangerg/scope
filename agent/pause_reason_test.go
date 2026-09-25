package agent

import (
	"bytes"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"strings"
	"testing"
)

func TestPauseReasonSurvivesControlAndRestoration(t *testing.T) {
	for _, test := range []struct {
		name   string
		reason string
	}{
		{name: "unicode", reason: "检查执行状态"},
		{name: "ascii boundary", reason: strings.Repeat("x", 4096)},
		{name: "unicode byte boundary", reason: strings.Repeat("界", 1365) + "x"},
		{name: "json expansion boundary", reason: strings.Repeat("\x00", 4096)},
	} {
		t.Run(test.name, func(t *testing.T) {
			transition, err := Pause(0, test.reason)
			if err != nil || !transition.Valid() {
				t.Fatalf("Pause transition: %v", err)
			}
			var parsed Transition
			if err := jsonv2.Unmarshal(controlValue(jsonv2.Marshal(transition)), &parsed); err != nil {
				t.Fatal(err)
			}
			if reason, ok := parsed.Reason(); !ok || reason != test.reason {
				t.Fatal("transition round trip changed the pause reason")
			}
			engine := controlValue(NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter()}))
			t.Cleanup(func() { mustCloseEngine(t, engine) })
			deployment := engineTestDeployment(t, newEngineTestDefinition(t, "engine.wait", "wait"), nil)
			process := controlValue(engine.Start(t.Context(), deployment, controlValue(EncodePayload(engineTestInput{Value: "pause"}))))
			t.Cleanup(func() {
				_ = process.Kill(context.WithoutCancel(t.Context()), "test complete")
				_ = process.Join(context.WithoutCancel(t.Context()))
			})
			waitForStatus(t, process, StatusWaiting)
			if err := process.Pause(t.Context(), test.reason); err != nil {
				t.Fatal(err)
			}
			waitForStatus(t, process, StatusPaused)
			tree := controlValue(ParseTreeSnapshot(controlValue(engine.CaptureTree(t.Context(), process.ID())).JSON()))
			paused := controlValue(ParseProcessSnapshot(tree.ProcessSnapshots()[0].JSON()))
			if wire := controlValue(paused.wire()); wire.PauseReason != test.reason || wire.Status != StatusPaused {
				t.Fatal("capture changed the pause reason or status")
			}
			restoredEngine := controlValue(NewEngine(EngineConfig{TreeCommitter: newSnapshotTestCommitter(tree)}))
			t.Cleanup(func() { mustCloseEngine(t, restoredEngine) })
			restored := controlValue(restoredEngine.RestoreTree(t.Context(), deployment, tree))
			t.Cleanup(func() {
				_ = restored.Kill(context.WithoutCancel(t.Context()), "test complete")
				_ = restored.Join(context.WithoutCancel(t.Context()))
			})
			if snapshot := inspectProcessSnapshot(t, restored); snapshot.Status() != StatusPaused ||
				controlValue(snapshot.wire()).PauseReason != test.reason {
				t.Fatal("restoration changed the pause reason or status")
			}
			if err := restored.Resume(t.Context()); err != nil {
				t.Fatal(err)
			}
			waitForStatus(t, restored, StatusWaiting)
		})
	}
}

func TestPauseReasonRejectsInvalidInputWithoutMutation(t *testing.T) {
	engine := controlValue(NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter()}))
	t.Cleanup(func() { mustCloseEngine(t, engine) })
	deployment := engineTestDeployment(t, newEngineTestDefinition(t, "engine.wait", "wait"), nil)
	process := controlValue(engine.Start(t.Context(), deployment, controlValue(EncodePayload(engineTestInput{Value: "pause"}))))
	t.Cleanup(func() {
		_ = process.Kill(context.WithoutCancel(t.Context()), "test complete")
		_ = process.Join(context.WithoutCancel(t.Context()))
	})
	waitForStatus(t, process, StatusWaiting)
	for _, test := range []struct {
		name   string
		reason string
	}{
		{name: "empty"},
		{name: "whitespace", reason: " \t"},
		{name: "leading space", reason: " pause"},
		{name: "trailing space", reason: "pause\n"},
		{name: "invalid utf8", reason: "\xff"},
		{name: "truncated utf8", reason: string([]byte{0xe7, 0x95})},
		{name: "ascii overflow", reason: strings.Repeat("x", 4097)},
		{name: "unicode byte overflow", reason: strings.Repeat("界", 1365) + "xx"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if transition, err := Pause(0, test.reason); transition.Valid() || !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("invalid Pause transition: %v, %v", transition, err)
			}
			before := controlValue(engine.CaptureTree(t.Context(), process.ID()))
			if err := process.Pause(t.Context(), test.reason); !errors.Is(err, ErrInvalidProcessControl) {
				t.Fatalf("invalid Host Pause: %v", err)
			}
			after := controlValue(engine.CaptureTree(t.Context(), process.ID()))
			if before.Digest() != after.Digest() {
				t.Fatal("invalid Host Pause changed tree state or resource usage")
			}
			wire := controlValue(before.ProcessSnapshots()[0].wire())
			wire.Status, wire.PauseReason = StatusPaused, test.reason
			if _, err := newProcessSnapshot(wire); !errors.Is(err, ErrInvalidSnapshot) {
				t.Fatalf("invalid current Pause capture: %v", err)
			}
			if encoded, err := jsonv2.Marshal(wire); err == nil {
				if _, err := ParseProcessSnapshot(encoded); !errors.Is(err, ErrInvalidSnapshot) {
					t.Fatalf("invalid current Pause parsing: %v", err)
				}
			}
			// An absent pending reason means there is no Pause request.
			if test.reason == "" {
				return
			}
			wire.Status, wire.PauseReason = StatusWaiting, ""
			wire.PendingControl.PauseReason = test.reason
			if _, err := newProcessSnapshot(wire); !errors.Is(err, ErrInvalidSnapshot) {
				t.Fatalf("invalid pending Pause capture: %v", err)
			}
			if encoded, err := jsonv2.Marshal(wire); err == nil {
				if _, err := ParseProcessSnapshot(encoded); !errors.Is(err, ErrInvalidSnapshot) {
					t.Fatalf("invalid pending Pause parsing: %v", err)
				}
			}
		})
	}
}

func TestPauseReservationPreservesCurrentAndPendingReasons(t *testing.T) {
	runtime := newWaitingSnapshotTree(t, 1)
	process := runtime.processes[runtime.rootID]
	process.status, process.pauseReason = StatusPaused, strings.Repeat("\x00", 4096)
	process.pendingControl.pauseReason = strings.Repeat("\x01", 4096)
	snapshot := controlValue(ParseProcessSnapshot(controlValue(process.capture()).JSON()))
	_, restored, _, err := prepareRestoredProcess(t.Context(), process.deployment, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(snapshot.JSON(), controlValue(restored.capture()).JSON()) {
		t.Fatal("restoration changed concurrent current and pending Pause reasons")
	}
	signal := mustMailboxSignal(t, "signal:pause-capacity", WaitID{}, []byte(`{"value":"input"}`))
	prospective, err := process.prepareSignals([]Signal{signal}, signalSourceExternal)
	if err != nil || prospective == nil {
		t.Fatalf("prepare capacity witness: %v", err)
	}
	wire := prospective.snapshotWire()
	// Keep the quota's decimal width fixed while measuring its own encoding.
	wire.Limits.MaxSnapshotBytes = NewQuota(999999)
	exact := controlValue(materializedAdmissionSize(wire))
	for _, maximum := range []uint64{exact, exact - 1} {
		process.limits.MaxSnapshotBytes = NewQuota(maximum)
		before := controlValue(process.capture())
		candidate, err := process.prepareSignals([]Signal{signal}, signalSourceExternal)
		if maximum == exact {
			if err != nil || candidate == nil {
				t.Fatalf("exact Pause reservation quota: %v", err)
			}
			if candidate.pauseReason != process.pauseReason || candidate.pendingControl.pauseReason != process.pendingControl.pauseReason {
				t.Fatal("admitted Signal changed concurrent Pause reasons")
			}
		} else if candidate != nil || !errors.Is(err, ErrResourceLimitExceeded) {
			t.Fatalf("one byte short: candidate=%v, error=%v", candidate != nil, err)
		}
		if after := controlValue(process.capture()); !bytes.Equal(before.JSON(), after.JSON()) {
			t.Fatal("unadopted admission changed Pause state or resource usage")
		}
	}
}
