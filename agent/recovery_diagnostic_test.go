package agent

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestProcessSnapshotReportsTheContradictedContract(t *testing.T) {
	runtime := newWaitingSnapshotTree(t, 1)
	snapshot := controlValue(runtime.captureTree()).ProcessSnapshots()[0]
	valid := controlValue(snapshot.wire())
	for _, test := range []struct {
		name   string
		mutate func(*processSnapshotWire)
		detail string
	}{
		{"identity", func(p *processSnapshotWire) { p.ProcessID = ProcessID{} }, "Process identity is invalid"},
		{"deployment", func(p *processSnapshotWire) { p.DeploymentRef = DeploymentRef{} }, "Deployment reference is invalid"},
		{"start", func(p *processSnapshotWire) { p.StartedAt = time.Time{} }, "Process start time is missing"},
		{"state", func(p *processSnapshotWire) { p.CommittedExecutionState = ExecutionState{} }, "committed Execution state is invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire := valid.clone()
			test.mutate(&wire)
			err := wire.validateContract()
			if !errors.Is(err, ErrInvalidSnapshot) || !strings.Contains(err.Error(), test.detail) {
				t.Fatalf("snapshot rejection=%v, want %q", err, test.detail)
			}
		})
	}
	t.Run("budget", func(t *testing.T) {
		wire := valid.clone()
		wire.Budget.Steps, wire.CommittedSteps = NewQuota(0), 1
		const detail = "usage and child allocations exceed the Process budget"
		if err := wire.validateCapacity(resourceAmounts{}); !errors.Is(err, ErrInvalidSnapshot) || !strings.Contains(err.Error(), detail) {
			t.Fatalf("snapshot rejection=%v, want %q", err, detail)
		}
	})
}

func TestTreeSnapshotReportsTheContradictedTreeLimit(t *testing.T) {
	valid := controlValue(controlValue(newWaitingSnapshotTree(t, 1).captureTree()).wire())
	for _, test := range []struct {
		name   string
		mutate func(*TreeLimits)
		detail string
	}{
		{"pending signals", func(l *TreeLimits) { l.MaxPendingSignals = 0 }, "TreeLimits: MaxPendingSignals must be greater than zero"},
		{"depth", func(l *TreeLimits) { l.MaxDepth = 0 }, "TreeLimits: MaxDepth must be greater than zero"},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire := valid.clone()
			test.mutate(&wire.TreeLimits)
			_, err := newTreeSnapshot(wire)
			if !errors.Is(err, ErrInvalidTreeSnapshot) || !strings.Contains(err.Error(), test.detail) {
				t.Fatalf("tree rejection=%v, want %q", err, test.detail)
			}
		})
	}
}

func TestPreparedEffectReportsContradictorySettlements(t *testing.T) {
	id := newProcessID().effectID(1, 0)
	succeeded := controlValue(NewSettlement(id, SettlementStatusSucceeded, []byte(`null`)))
	unknown := controlValue(NewSettlement(id, SettlementStatusUnknown, []byte(`null`)))
	foreign := controlValue(NewSettlement(newProcessID().effectID(1, 0), SettlementStatusSucceeded, []byte(`null`)))
	invalid := Settlement{}
	for _, test := range []struct {
		name       string
		record     preparedEffect
		transition func(*preparedEffect) error
		detail     string
	}{
		{"invalid settlement", preparedEffect{progress: &effectProgress{settlement: &invalid}}, (*preparedEffect).validatePhase, "prepared Effect settlement is invalid"},
		{"settle twice", preparedEffect{progress: &effectProgress{settlement: &succeeded}}, func(p *preparedEffect) error { return p.settle(succeeded, nil) }, "effect is not pending"},
		{"settle invalid", preparedEffect{progress: &effectProgress{}}, func(p *preparedEffect) error { return p.settle(invalid, nil) }, "incoming settlement is invalid"},
		{"settle foreign", preparedEffect{progress: &effectProgress{}}, func(p *preparedEffect) error { return p.settle(foreign, nil) }, "incoming settlement identifies another Effect"},
		{"resolve definite", preparedEffect{progress: &effectProgress{settlement: &succeeded}}, func(p *preparedEffect) error { return p.resolveUnknown(succeeded) }, "effect outcome is already definite"},
		{"resolve invalid", preparedEffect{progress: &effectProgress{settlement: &unknown}}, func(p *preparedEffect) error { return p.resolveUnknown(invalid) }, "resolution must supply a definite settlement"},
		{"resolve unknown", preparedEffect{progress: &effectProgress{settlement: &unknown}}, func(p *preparedEffect) error { return p.resolveUnknown(unknown) }, "resolution must supply a definite settlement"},
		{"resolve foreign", preparedEffect{progress: &effectProgress{settlement: &unknown}}, func(p *preparedEffect) error { return p.resolveUnknown(foreign) }, "resolution identifies another Effect"},
	} {
		t.Run(test.name, func(t *testing.T) {
			record := test.record
			record.ID = id
			err := test.transition(&record)
			if err == nil || err.Error() != test.detail {
				t.Fatalf("rejection=%v, want %q", err, test.detail)
			}
			if record.phase() != test.record.phase() || record.settlement() != test.record.settlement() {
				t.Fatal("rejected transition changed the Effect outcome")
			}
		})
	}
}
