package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func ExampleQuota() {
	unlimited := Quota{}
	finite := NewQuota(3)
	zero := NewQuota(0)
	fmt.Println(unlimited.Allows(10001), finite.Allows(2, 1), finite.Allows(3, 1), zero.Allows(1))
	// Output: true true false false
}

func TestTreeQuotaMustAdmitRoot(t *testing.T) {
	if _, err := NewEngine(EngineConfig{TreeLimits: TreeLimits{MaxTreeProcesses: NewQuota(0)}}); !errors.Is(err, ErrInvalidEngineConfig) {
		t.Fatalf("empty tree allowance accepted: %v", err)
	}
}

func TestMailboxCapacityFitsTransitionConsumption(t *testing.T) {
	if _, err := NewEngine(EngineConfig{Limits: Limits{MaxPendingSignals: uint64(^uint32(0)) + 1}}); !errors.Is(err, ErrInvalidEngineConfig) {
		t.Fatalf("unrepresentable mailbox capacity accepted: %v", err)
	}
	wire := controlValue(preparedEngineTestSnapshot(t).wire())
	wire.MaxPendingSignals = uint64(^uint32(0)) + 1
	if _, err := processSnapshotFromWire(wire); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("unrepresentable restored capacity accepted: %v", err)
	}
}

func TestRestoredChildPreservesInheritedCapacityAndParentAuthority(t *testing.T) {
	runtime := newWaitingSnapshotTree(t, 2)
	for _, test := range []struct {
		name   string
		mutate func(*processSnapshotWire)
	}{
		{"mailbox capacity", func(wire *processSnapshotWire) { wire.MaxPendingSignals++ }},
		{"snapshot capacity", func(wire *processSnapshotWire) { wire.MaxSnapshotBytes = NewQuota(100000) }},
		{"unlimited grant under finite parent", func(wire *processSnapshotWire) { wire.Budget = Budget{} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := controlValue(runtime.captureTree())
			wire := controlValue(snapshot.wire())
			child := wire.ProcessSnapshots[1].state
			test.mutate(&child)
			wire.ProcessSnapshots[1] = controlValue(processSnapshotFromWire(child))
			if _, err := newTreeSnapshot(wire); !errors.Is(err, ErrInvalidTreeSnapshot) {
				t.Fatalf("invalid child authority accepted: %v", err)
			}
		})
	}
}

func TestQuotaPreservesUnlimitedFiniteAndZero(t *testing.T) {
	schema := controlValue(SchemaFor[Quota]())
	for _, test := range []struct {
		name      string
		quota     Quota
		wire      string
		allowsOne bool
	}{
		{"unlimited", Quota{}, `{"maximum":null}`, true},
		{"zero", NewQuota(0), `{"maximum":0}`, false},
		{"finite", NewQuota(3), `{"maximum":3}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(test.quota)
			if err != nil || string(encoded) != test.wire {
				t.Fatalf("encoding=%s error=%v", encoded, err)
			}
			if validateErr := schema.Validate(encoded); validateErr != nil {
				t.Fatalf("quota schema rejected its encoding: %v", validateErr)
			}
			var restored Quota
			if err := json.Unmarshal(encoded, &restored); err != nil || restored != test.quota {
				t.Fatalf("round trip=%+v error=%v", restored, err)
			}
			if restored.Allows(1) != test.allowsOne {
				t.Fatal("quota changed its bound")
			}
		})
	}
	for _, data := range []string{`0`, `null`, `{}`, `{"maximum":-1}`, `{"maximum":1,"extra":true}`, `{"maximum":18446744073709551616}`} {
		var quota Quota
		if err := json.Unmarshal([]byte(data), &quota); err == nil {
			t.Fatalf("invalid quota accepted: %s", data)
		}
	}
	if !NewQuota(^uint64(0)).Allows(^uint64(0)-1, 1) || NewQuota(^uint64(0)).Allows(^uint64(0), 1) {
		t.Fatal("finite quota arithmetic overflowed")
	}
	if !(Quota{}).Allows(^uint64(0), ^uint64(0)) {
		t.Fatal("unlimited quota became a machine-sized budget")
	}
}

func TestBudgetAllocationIsIndependentPerDimension(t *testing.T) {
	finite := Budget{Steps: NewQuota(10), Effects: NewQuota(10), Signals: NewQuota(10)}
	child := Budget{Steps: NewQuota(3), Effects: NewQuota(4), Signals: NewQuota(5)}
	for _, test := range []struct {
		name          string
		parent, child Budget
		debit         resourceAmounts
		allowed       bool
	}{
		{"finite finite", finite, child, resourceAmounts{Steps: 3, Effects: 4, Signals: 5}, true},
		{"unlimited finite", Budget{}, child, resourceAmounts{}, true},
		{"unlimited unlimited", Budget{}, Budget{}, resourceAmounts{}, true},
		{"finite unlimited", finite, Budget{}, resourceAmounts{}, false},
		{"mixed", Budget{Effects: NewQuota(10)}, Budget{Effects: NewQuota(4)}, resourceAmounts{Effects: 4}, true},
		{"mixed escalation", Budget{Effects: NewQuota(10)}, Budget{}, resourceAmounts{}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			debit, ok := test.parent.allocation(test.child)
			if ok != test.allowed || ok && debit != test.debit {
				t.Fatalf("debit=%+v allowed=%t", debit, ok)
			}
		})
	}
	if !finite.canAllocate(Usage{CommittedSteps: 6}, resourceAmounts{Steps: 1}, child) || finite.canAllocate(Usage{CommittedSteps: 7}, resourceAmounts{Steps: 1}, child) {
		t.Fatal("prepared parent work was not reserved at the exact boundary")
	}
}

func TestChildAdmissionRejectsUnlimitedAuthorityFromFiniteParent(t *testing.T) {
	runtime, parent := newChildCompletionTestProcess(t)
	if err := runtime.engine.reserveProcessStart(parent.handle.relation, parent.deployment.DeploymentRef(), parent.treeLimits, Digest{}); err != nil {
		t.Fatal(err)
	}
	runtime.engine.publishProcessStart(parent.handle)
	t.Cleanup(func() {
		parent.requestCancellation(controlValue(newCancellationIntent(cancellationOwnerHost, "test finished")))
		runtime.installTermination(parent, stepOutcome{})
		runtime.finishIfTerminal(parent)
		runtime.publishJoins()
	})
	spec := childTestSpec(controlValue(ParseChildKey("unlimited")), parent.deployment.DeploymentRef(), controlValue(EncodePayload(childTestInput{Mode: "leaf"})))
	spec.Budget = Budget{}
	effectID := parent.handle.processID.effectID(1, 0)
	rejected := runtime.prepareChildStart(parent, effectID, spec)
	failure, failed := rejected.result.Failure()
	if rejected.plan != nil || !failed || failure.Code() != failureCodeEngineChildBudgetExhausted || parent.provisionalChildBudget != nil {
		t.Fatalf("finite parent admitted unlimited grant: %+v", rejected)
	}
	parent.budget = Budget{}
	accepted := runtime.prepareChildStart(parent, effectID, spec)
	if accepted.plan == nil || accepted.plan.limits.budget() != (Budget{}) {
		t.Fatalf("unlimited parent rejected grant: %+v", accepted)
	}
	runtime.discardChildStart(accepted.plan)
	if parent.provisionalChildBudget != nil || parent.allocatedResources != (resourceAmounts{}) {
		t.Fatal("discard retained an unlimited allocation")
	}
}

func TestQuotaConfigurationIdentityDistinguishesAllModes(t *testing.T) {
	definition := newChildTestDeployment(t).Definition()
	seen := make(map[DeploymentRef]bool)
	for _, quota := range []Quota{{}, NewQuota(0), NewQuota(1)} {
		config := controlValue(json.Marshal(struct {
			ModelCalls Quota `json:"model_calls"`
		}{ModelCalls: quota}))
		deployment := controlValue(NewDeployment(DeploymentConfig{Definition: definition,
			ImplementationDigest: ComputeDigest([]byte("quota-contract")), ConfigurationDigest: ComputeDigest(config)}))
		if seen[deployment.DeploymentRef()] {
			t.Fatal("finite, zero, and unlimited quotas share a configuration identity")
		}
		seen[deployment.DeploymentRef()] = true
	}
}

func TestUnlimitedChildReservationRollbackPreservesExistingAllocation(t *testing.T) {
	parent := &processState{budget: Budget{Effects: NewQuota(10)}}
	first := Budget{Effects: NewQuota(3)}
	second := Budget{Effects: NewQuota(4)}
	for _, child := range []Budget{first, second} {
		if !parent.reserveProvisionalChildBudget(child) {
			t.Fatal("unlimited grant rejected")
		}
		if err := parent.commitProvisionalChildBudget(child); err != nil {
			t.Fatal(err)
		}
	}
	parent.releaseCommittedChildBudget(second)
	if parent.allocatedResources != (resourceAmounts{Effects: 3}) {
		t.Fatalf("rollback lost existing grant: %+v", parent.allocatedResources)
	}
	if !parent.reserveProvisionalChildBudget(Budget{Effects: NewQuota(7)}) {
		t.Fatal("released finite debit remained charged")
	}
	if parent.reserveProvisionalChildBudget(Budget{}) {
		t.Fatal("second provisional grant overwrote the first")
	}
	parent.releaseProvisionalChildBudget(Budget{Effects: NewQuota(7)})
	if parent.provisionalChildBudget != nil || parent.allocatedResources.Effects != 3 {
		t.Fatal("provisional rollback changed published allocation")
	}
}

func TestUnlimitedExecutionCountersStopBeforeWrap(t *testing.T) {
	_, process := newChildCompletionTestProcess(t)
	process.budget = Budget{}
	process.committedSteps = ^uint64(0)
	if failure := process.stepSchedulingFailure(); failure == nil || !errors.Is(failure.cause, ErrCounterExhausted) {
		t.Fatalf("step overflow=%+v", failure)
	}
	if process.committedSteps != ^uint64(0) {
		t.Fatal("step sequence wrapped")
	}
	process.committedSteps = 0
	process.counters.PreparedEffects = ^uint64(0)
	effect := controlValue(NewWaitEffect(controlValue(ParseWaitKey("counter")), json.RawMessage(`{}`)))
	failure := prepareTestStep(process, stepJobResult{transition: controlValue(Continue(0, effect)), candidate: process.execution, candidateState: process.committedExecutionState})
	if failure == nil || !errors.Is(failure.cause, ErrCounterExhausted) || process.prepared != nil || process.counters.PreparedEffects != ^uint64(0) {
		t.Fatalf("effect overflow mutated state: %+v", failure)
	}
}

func TestSnapshotRejectsMissingAndLegacyQuotaAuthority(t *testing.T) {
	snapshot := preparedEngineTestSnapshot(t)
	for _, name := range []string{"budget", "allocated_resources", "max_snapshot_bytes"} {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(snapshot.JSON(), &fields); err != nil {
			t.Fatal(err)
		}
		delete(fields, name)
		if _, err := ParseProcessSnapshot(controlValue(json.Marshal(fields))); !errors.Is(err, ErrInvalidSnapshot) {
			t.Fatalf("missing %s accepted: %v", name, err)
		}
	}
	for _, data := range []string{`{}`, `{"steps":1,"effects":1,"signals":1}`, `{"steps":{"maximum":null},"effects":{"maximum":null}}`} {
		var budget Budget
		if err := json.Unmarshal([]byte(data), &budget); err == nil {
			t.Fatalf("invalid grant accepted: %s", data)
		}
	}
}
