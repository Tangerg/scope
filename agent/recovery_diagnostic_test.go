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
		{"status", func(p *processSnapshotWire) { p.Status = StatusInvalid }, "Process status is invalid"},
		{"state", func(p *processSnapshotWire) { p.CommittedExecutionState = ExecutionState{} }, "committed Execution state is invalid"},
		{"limits", func(p *processSnapshotWire) { p.Limits.MaxPendingSignals = 0 }, "Limits: MaxPendingSignals must be greater than zero"},
		{"tree limits", func(p *processSnapshotWire) { p.TreeLimits.MaxDepth = 0 }, "TreeLimits: MaxDepth must be greater than zero"},
		{"budget", func(p *processSnapshotWire) { p.Limits.Budget.Steps = NewQuota(0); p.CommittedSteps = 1 }, "usage and child allocations exceed the Process budget"},
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
		{"unknown phase", preparedEffect{}, (*preparedEffect).validatePhase, "prepared Effect phase is invalid"},
		{"missing settlement", preparedEffect{Phase: effectPhaseSettled}, (*preparedEffect).validatePhase, "prepared Effect settlement presence disagrees with phase"},
		{"invalid settlement", preparedEffect{Phase: effectPhaseSettled, Settlement: &invalid}, (*preparedEffect).validatePhase, "prepared Effect settlement is invalid"},
		{"foreign settlement", preparedEffect{Phase: effectPhaseSettled, Settlement: &foreign}, (*preparedEffect).validatePhase, "prepared Effect settlement identifies another Effect"},
		{"settle twice", preparedEffect{Phase: effectPhasePending, Settlement: &succeeded}, func(p *preparedEffect) error { return p.settle(succeeded, nil) }, "pending Effect already has a settlement"},
		{"settle invalid", preparedEffect{Phase: effectPhasePending}, func(p *preparedEffect) error { return p.settle(invalid, nil) }, "incoming settlement is invalid"},
		{"settle foreign", preparedEffect{Phase: effectPhasePending}, func(p *preparedEffect) error { return p.settle(foreign, nil) }, "incoming settlement identifies another Effect"},
		{"resolve definite", preparedEffect{Phase: effectPhaseSettled, Settlement: &succeeded}, func(p *preparedEffect) error { return p.resolveUnknown(succeeded) }, "effect outcome is already definite"},
		{"resolve invalid", preparedEffect{Phase: effectPhaseSettled, Settlement: &unknown}, func(p *preparedEffect) error { return p.resolveUnknown(invalid) }, "resolution must supply a definite settlement"},
		{"resolve unknown", preparedEffect{Phase: effectPhaseSettled, Settlement: &unknown}, func(p *preparedEffect) error { return p.resolveUnknown(unknown) }, "resolution must supply a definite settlement"},
		{"resolve foreign", preparedEffect{Phase: effectPhaseSettled, Settlement: &unknown}, func(p *preparedEffect) error { return p.resolveUnknown(foreign) }, "resolution identifies another Effect"},
	} {
		t.Run(test.name, func(t *testing.T) {
			record := test.record
			record.ID = id
			err := test.transition(&record)
			if err == nil || err.Error() != test.detail {
				t.Fatalf("rejection=%v, want %q", err, test.detail)
			}
			if record.Phase != test.record.Phase || record.Settlement != test.record.Settlement {
				t.Fatal("rejected transition changed the Effect outcome")
			}
		})
	}
}
