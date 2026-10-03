package agent

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"math"
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
	if _, err := NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter(), TreeLimits: TreeLimits{MaxTreeProcesses: NewQuota(0)}}); !errors.Is(err, ErrInvalidEngineConfig) {
		t.Fatalf("empty tree allowance accepted: %v", err)
	}
}

func TestMailboxCapacityFitsTransitionConsumption(t *testing.T) {
	if _, err := NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter(), TreeLimits: TreeLimits{MaxPendingSignals: uint64(^uint32(0)) + 1}}); !errors.Is(err, ErrInvalidEngineConfig) {
		t.Fatalf("unrepresentable mailbox capacity accepted: %v", err)
	}
	wire := controlValue(controlValue(newWaitingSnapshotTree(t, 1).captureTree()).wire())
	wire.TreeLimits.MaxPendingSignals = math.MaxUint32 + 1
	if _, err := newTreeSnapshot(wire); !errors.Is(err, ErrInvalidTreeSnapshot) {
		t.Fatalf("unrepresentable restored capacity accepted: %v", err)
	}
}

func TestRestoredChildCannotExceedParentAuthority(t *testing.T) {
	snapshot := controlValue(newWaitingSnapshotTree(t, 2).captureTree())
	wire := controlValue(snapshot.wire())
	child := wire.ProcessSnapshots[1].state
	child.Budget = Budget{}
	wire.ProcessSnapshots[1] = controlValue(processSnapshotFromWire(child))
	if _, err := newTreeSnapshot(wire); !errors.Is(err, ErrInvalidTreeSnapshot) {
		t.Fatalf("unlimited grant under finite parent accepted: %v", err)
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
			encoded, err := jsonv2.Marshal(test.quota)
			if err != nil || string(encoded) != test.wire {
				t.Fatalf("encoding=%s error=%v", encoded, err)
			}
			if validateErr := schema.Validate(encoded); validateErr != nil {
				t.Fatalf("quota schema rejected its encoding: %v", validateErr)
			}
			var restored Quota
			if err := jsonv2.Unmarshal(encoded, &restored); err != nil || restored != test.quota {
				t.Fatalf("round trip=%+v error=%v", restored, err)
			}
			if restored.Allows(1) != test.allowsOne {
				t.Fatal("quota changed its bound")
			}
		})
	}
	for _, data := range []string{`0`, `null`, `{}`, `{"maximum":-1}`, `{"maximum":1,"extra":true}`, `{"maximum":18446744073709551616}`} {
		var quota Quota
		if err := jsonv2.Unmarshal([]byte(data), &quota); err == nil {
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
	if err := runtime.engine.reserveProcessStart(parent.handle.relation, parent.deployment().DeploymentRef()); err != nil {
		t.Fatal(err)
	}
	runtime.engine.publishProcessStart(parent.handle)
	t.Cleanup(func() {
		parent.requestCancellation(controlValue(newCancellationIntent(cancellationOwnerHost, "test finished")))
		runtime.installTermination(parent, stepOutcome{})
		runtime.finishIfTerminal(parent)
		runtime.tryStartCheckpoint()
		if runtime.writer.committing() {
			runtime.applyTreeCommitCompletion(<-runtime.writer.done)
		}
		runtime.publishJoins()
	})
	spec := childTestSpec(controlValue(ParseChildKey("unlimited")), parent.deployment().DeploymentRef(), controlValue(EncodePayload(childTestInput{Mode: "leaf"})))
	spec.Budget = Budget{}
	effectID := parent.handle.processID.effectID(1, 0)
	rejected := runtime.prepareChildStart(parent, effectID, spec)
	failure, failed := rejected.result.Failure()
	if rejected.plan != nil || !failed || failure.Code() != failureCodeEngineChildBudgetExhausted || parent.provisionalChildBudget != nil {
		t.Fatalf("finite parent admitted unlimited grant: %+v", rejected)
	}
	parent.handle.budget = Budget{}
	accepted := runtime.prepareChildStart(parent, effectID, spec)
	if accepted.plan == nil || accepted.plan.spec.Budget != (Budget{}) {
		t.Fatalf("unlimited parent rejected grant: %+v", accepted)
	}
	runtime.discardChildStart(accepted.plan)
	if parent.provisionalChildBudget != nil || runtime.members.childAllocation(parent.handle.processID) != (resourceAmounts{}) {
		t.Fatal("discard retained an unlimited allocation")
	}
}

func TestQuotaConfigurationIdentityDistinguishesAllModes(t *testing.T) {
	definition := newChildTestDeployment(t).Definition()
	seen := make(map[DeploymentRef]bool)
	for _, quota := range []Quota{{}, NewQuota(0), NewQuota(1)} {
		config := controlValue(jsonv2.Marshal(struct {
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
	parentID := newProcessID()
	parentRelation := rootProcessRelation(parentID)
	parent := &processState{handle: &processHandle{processID: parentID, relation: parentRelation, budget: Budget{Effects: NewQuota(10)}}}
	members := newTreeMembers(3)
	members.add(parent)
	var secondID ProcessID
	for index, budget := range []Budget{{Effects: NewQuota(3)}, {Effects: NewQuota(4)}} {
		childID := newProcessID()
		key := controlValue(ParseChildKey(fmt.Sprintf("child-%d", index)))
		members.add(&processState{handle: &processHandle{
			processID: childID, relation: childProcessRelation(childID, parentRelation, key), budget: budget,
		}})
		secondID = childID
	}
	members.remove(secondID)
	if allocated := members.childAllocation(parentID); allocated != (resourceAmounts{Effects: 3}) {
		t.Fatalf("rollback lost existing grant: %+v", allocated)
	}
	if !parent.reserveProvisionalChildBudget(Budget{Effects: NewQuota(7)}, members.childAllocation(parentID)) {
		t.Fatal("released finite debit remained charged")
	}
	if parent.reserveProvisionalChildBudget(Budget{}, members.childAllocation(parentID)) {
		t.Fatal("second provisional grant overwrote the first")
	}
	parent.releaseProvisionalChildBudget(Budget{Effects: NewQuota(7)})
	if parent.provisionalChildBudget != nil || members.childAllocation(parentID).Effects != 3 {
		t.Fatal("provisional rollback changed published allocation")
	}
}

func TestUnlimitedExecutionCountersStopBeforeWrap(t *testing.T) {
	runtime, process := newChildCompletionTestProcess(t)
	process.handle.budget = Budget{}
	process.committedSteps = ^uint64(0)
	if failure := process.stepSchedulingFailure(resourceAmounts{}); failure == nil || !errors.Is(failure.cause, ErrCounterExhausted) {
		t.Fatalf("step overflow=%+v", failure)
	}
	if process.committedSteps != ^uint64(0) {
		t.Fatal("step sequence wrapped")
	}
	process.committedSteps = 0
	process.counters.PreparedEffects = ^uint64(0)
	effect := controlValue(NewWaitEffect(controlValue(ParseWaitKey("counter"))))
	failure := prepareTestStep(process, runtime.treeLimits, stepJobResult{transition: controlValue(Continue(0, effect)), candidate: process.execution, candidateState: process.committedExecutionState})
	if failure == nil || !errors.Is(failure.cause, ErrCounterExhausted) || process.prepared != nil || process.counters.PreparedEffects != ^uint64(0) {
		t.Fatalf("effect overflow mutated state: %+v", failure)
	}
}

func TestSnapshotRejectsMissingAndRetiredQuotaAuthority(t *testing.T) {
	snapshot := preparedEngineTestSnapshot(t)
	var fields map[string]json.RawMessage
	if err := jsonv2.Unmarshal(snapshot.JSON(), &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "budget")
	if _, err := parseTestProcessSnapshot(controlValue(jsonv2.Marshal(fields))); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("missing budget accepted: %v", err)
	}
	for _, name := range []string{"limits", "tree_limits", "allocated_resources"} {
		var fields map[string]json.RawMessage
		if err := jsonv2.Unmarshal(snapshot.JSON(), &fields); err != nil {
			t.Fatal(err)
		}
		fields[name] = controlValue(jsonv2.Marshal(DefaultTreeLimits()))
		if _, err := parseTestProcessSnapshot(controlValue(jsonv2.Marshal(fields))); !errors.Is(err, ErrInvalidSnapshot) {
			t.Fatalf("retired per-Process %s accepted: %v", name, err)
		}
	}
	for _, data := range []string{`{}`, `{"steps":1,"effects":1,"signals":1}`, `{"steps":{"maximum":null},"effects":{"maximum":null}}`} {
		var budget Budget
		if err := jsonv2.Unmarshal([]byte(data), &budget); err == nil {
			t.Fatalf("invalid grant accepted: %s", data)
		}
	}
}

func TestTreeSnapshotRequiresCompleteTreeLimits(t *testing.T) {
	snapshot := controlValue(newWaitingSnapshotTree(t, 1).captureTree())
	var tree map[string]json.RawMessage
	if err := jsonv2.Unmarshal(snapshot.JSON(), &tree); err != nil {
		t.Fatal(err)
	}
	withoutLimits := maps.Clone(tree)
	delete(withoutLimits, "tree_limits")
	if _, err := ParseTreeSnapshot(controlValue(jsonv2.Marshal(withoutLimits))); !errors.Is(err, ErrInvalidTreeSnapshot) {
		t.Fatalf("tree without TreeLimits accepted: %v", err)
	}
	var limits map[string]json.RawMessage
	if err := jsonv2.Unmarshal(tree["tree_limits"], &limits); err != nil {
		t.Fatal(err)
	}
	for name := range limits {
		for _, replacement := range []json.RawMessage{nil, json.RawMessage(`null`)} {
			t.Run(name+"/"+string(replacement), func(t *testing.T) {
				mutated := maps.Clone(limits)
				if replacement == nil {
					delete(mutated, name)
				} else {
					mutated[name] = replacement
				}
				fields := maps.Clone(tree)
				fields["tree_limits"] = controlValue(jsonv2.Marshal(mutated))
				if _, err := ParseTreeSnapshot(controlValue(jsonv2.Marshal(fields))); !errors.Is(err, ErrInvalidTreeSnapshot) {
					t.Fatalf("missing/null %s accepted: %v", name, err)
				}
			})
		}
	}
}

func TestBudgetUsesOneStrictRepresentation(t *testing.T) {
	budget := Budget{Steps: NewQuota(5), Effects: NewQuota(7), Signals: NewQuota(9)}
	encoded := controlValue(jsonv2.Marshal(budget))
	var decoded Budget
	if err := jsonv2.Unmarshal(encoded, &decoded); err != nil || decoded != budget {
		t.Fatalf("budget round trip=%+v, %v", decoded, err)
	}
	for _, field := range []string{"max_steps", "max_effects", "max_signals"} {
		var fields map[string]json.RawMessage
		if err := jsonv2.Unmarshal(encoded, &fields); err != nil {
			t.Fatal(err)
		}
		fields[field] = controlValue(jsonv2.Marshal(NewQuota(1)))
		if err := jsonv2.Unmarshal(controlValue(jsonv2.Marshal(fields)), &decoded); err == nil {
			t.Fatalf("retired quota field %s accepted", field)
		}
	}
}
