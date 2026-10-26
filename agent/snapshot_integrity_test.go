package agent

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

func TestTreeSnapshotRejectsContradictoryRelationshipsAndAccounting(t *testing.T) {
	base := controlValue(newWaitingSnapshotTree(t, 3).captureTree())
	for _, test := range []struct {
		name   string
		mutate func(*treeSnapshotDocument, []processSnapshotDocument)
		want   string
	}{
		{"duplicate process", func(_ *treeSnapshotDocument, processes []processSnapshotDocument) {
			processes[2] = processes[1]
		}, "duplicate ProcessID"},
		{"no root", func(_ *treeSnapshotDocument, processes []processSnapshotDocument) {
			processes[0].ParentID = new(processes[1].ProcessID)
			processes[0].ChildKey = new(controlValue(ParseChildKey("root")))
		}, "root Process is missing"},
		{"missing parent", func(_ *treeSnapshotDocument, processes []processSnapshotDocument) {
			processes[1].ParentID = new(controlValue(ParseProcessID("missing-parent")))
		}, "Process does not descend from the root"},
		{"parent cycle", func(_ *treeSnapshotDocument, processes []processSnapshotDocument) {
			processes[1].ParentID = new(processes[2].ProcessID)
			processes[2].ParentID = new(processes[1].ProcessID)
		}, "Process does not descend from the root"},
		{"parent without key", func(_ *treeSnapshotDocument, processes []processSnapshotDocument) {
			processes[1].ChildKey = nil
		}, "a child needs both its parent and ChildKey"},
		{"root with child key", func(_ *treeSnapshotDocument, processes []processSnapshotDocument) {
			processes[0].ChildKey = new(controlValue(ParseChildKey("unowned")))
		}, "a child needs both its parent and ChildKey"},
		{"duplicate child key", func(_ *treeSnapshotDocument, processes []processSnapshotDocument) {
			processes[2].ChildKey = processes[1].ChildKey
		}, "duplicate parent-scoped ChildKey"},
		{"escalated child capabilities", func(_ *treeSnapshotDocument, processes []processSnapshotDocument) {
			processes[1].Capabilities = controlValue(NewCapabilitySet(controlValue(ParseCapability("storage.write"))))
		}, "invalid child relation or attenuation"},
		{"unlimited grant from finite parent", func(_ *treeSnapshotDocument, processes []processSnapshotDocument) {
			processes[1].Budget.Steps = Quota{}
		}, "child grant exceeds parent authority"},
		{"child grants overflow", func(_ *treeSnapshotDocument, processes []processSnapshotDocument) {
			for index := range processes {
				processes[index].Budget.Steps = NewQuota(math.MaxUint64)
			}
		}, "child budget overflow"},
		{"child grants exceed parent budget", func(_ *treeSnapshotDocument, processes []processSnapshotDocument) {
			processes[0].Budget.Steps = NewQuota(15)
		}, "usage and child allocations exceed the Process budget"},
		{"tree cardinality", func(document *treeSnapshotDocument, _ []processSnapshotDocument) {
			document.TreeLimits.MaxTreeProcesses = NewQuota(2)
		}, "invalid root or tree size"},
		{"lifetime child limit", func(document *treeSnapshotDocument, _ []processSnapshotDocument) {
			document.TreeLimits.MaxChildren = NewQuota(1)
		}, "child limits exceeded"},
		{"active child limit", func(document *treeSnapshotDocument, _ []processSnapshotDocument) {
			document.TreeLimits.MaxActiveChildren = 1
		}, "child limits exceeded"},
		{"process byte limit", func(document *treeSnapshotDocument, _ []processSnapshotDocument) {
			document.TreeLimits.MaxProcessSnapshotBytes = NewQuota(1)
		}, "snapshot byte quota exceeded"},
		{"tree byte limit", func(document *treeSnapshotDocument, _ []processSnapshotDocument) {
			document.TreeLimits.MaxSnapshotBytes = NewQuota(1)
		}, "snapshot byte quota exceeded"},
		{"invalid captured limits", func(document *treeSnapshotDocument, _ []processSnapshotDocument) {
			document.TreeLimits.MaxDepth = 0
		}, "TreeLimits"},
		{"missing process start time", func(_ *treeSnapshotDocument, processes []processSnapshotDocument) {
			processes[1].StartedAt = time.Time{}
		}, "Process start time is missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := controlValue(jsonwire.Decode[treeSnapshotDocument](base.JSON()))
			processes := make([]processSnapshotDocument, len(document.ProcessSnapshots))
			for index, raw := range document.ProcessSnapshots {
				processes[index] = controlValue(decodeProcessSnapshotDocument(raw))
			}
			test.mutate(&document, processes)
			for index, process := range processes {
				document.ProcessSnapshots[index] = controlValue(jsonv2.Marshal(process))
			}
			candidate := controlValue(jsonv2.Marshal(document))
			accepted := base
			if err := accepted.UnmarshalJSON(candidate); !errors.Is(err, ErrInvalidTreeSnapshot) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("corrupt snapshot = %v, want %q", err, test.want)
			}
			if accepted.Digest() != base.Digest() || !bytes.Equal(accepted.JSON(), base.JSON()) {
				t.Fatal("rejected snapshot replaced the last accepted tree")
			}
		})
	}
}

func TestPreparedSnapshotRejectsContradictoryEffectEvidence(t *testing.T) {
	base := preparedEngineTestSnapshot(t)
	failure := controlValue(NewFailure(FailureKindExternal, "test.dispatch.failed", "The operation failed."))
	for _, test := range []struct {
		name   string
		mutate func(*preparedEffectWire)
		want   string
	}{
		{"missing request", func(effect *preparedEffectWire) { effect.Effect = nil }, "exactly one request or started child"},
		{"two request owners", func(effect *preparedEffectWire) {
			effect.StartedChild = &startedChildWire{Input: controlValue(EncodePayload("input"))}
		}, "exactly one request or started child"},
		{"started child without settlement", func(effect *preparedEffectWire) {
			effect.Effect = nil
			effect.StartedChild = &startedChildWire{Input: controlValue(EncodePayload("input"))}
		}, "started child requires its successful settlement"},
		{"started child with failure", func(effect *preparedEffectWire) {
			effect.Effect = nil
			effect.StartedChild = &startedChildWire{Input: controlValue(EncodePayload("input"))}
			effect.Progress = &effectProgressWire{Settlement: &preparedSettlementWire{Failure: &failure}}
		}, "started child requires its successful settlement"},
		{"dispatcher with framework failure", func(effect *preparedEffectWire) {
			effect.Progress = &effectProgressWire{Settlement: &preparedSettlementWire{Failure: &failure}}
		}, "Dispatcher settlement stores a Framework failure"},
		{"diagnostic without outcome", func(effect *preparedEffectWire) {
			effect.Progress = &effectProgressWire{Diagnostic: &failure}
		}, "invalid effect diagnostic"},
		{"outcome without status", func(effect *preparedEffectWire) {
			effect.Progress = &effectProgressWire{Settlement: &preparedSettlementWire{Payload: json.RawMessage(`null`)}}
		}, "invalid effect settlement"},
		{"outcome without payload", func(effect *preparedEffectWire) {
			effect.Progress = &effectProgressWire{Settlement: &preparedSettlementWire{Status: SettlementStatusSucceeded}}
		}, "invalid effect settlement"},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := controlValue(decodeProcessSnapshotDocument(base.JSON()))
			test.mutate(&document.Prepared.Effects[0])
			if _, err := parseTestProcessSnapshot(controlValue(jsonv2.Marshal(document))); !errors.Is(err, ErrInvalidSnapshot) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("corrupt effect evidence = %v, want %q", err, test.want)
			}
		})
	}
}

func TestTerminalSnapshotRejectsUnretiredSchedulingAndControlFacts(t *testing.T) {
	base := completedTreeSnapshot(t).ProcessSnapshots()[0]
	for _, test := range []struct {
		name   string
		mutate func(*processSnapshotDocument)
		want   string
	}{
		{"pause", func(p *processSnapshotDocument) { p.PauseReason = "still paused" }, "cannot retain a pause or current wait"},
		{"current wait", func(p *processSnapshotDocument) { p.CurrentWaitID = new(controlValue(ParseWaitID("wait:retained"))) }, "cannot retain a pause or current wait"},
		{"pending control", func(p *processSnapshotDocument) { p.PendingControl.KillReason = "still pending" }, "cannot retain control state"},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := controlValue(decodeProcessSnapshotDocument(base.JSON()))
			test.mutate(&document)
			if _, err := parseTestProcessSnapshot(controlValue(jsonv2.Marshal(document))); !errors.Is(err, ErrInvalidSnapshot) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("terminal scheduling evidence accepted or misreported: %v, want %s", err, test.want)
			}
		})
	}
}

func TestPreparedSnapshotCannotAlsoClaimAnAdoptedPause(t *testing.T) {
	document := controlValue(decodeProcessSnapshotDocument(preparedEngineTestSnapshot(t).JSON()))
	document.PauseReason = "candidate was already paused"
	if _, err := parseTestProcessSnapshot(controlValue(jsonv2.Marshal(document))); !errors.Is(err, ErrInvalidSnapshot) || !strings.Contains(err.Error(), "prepared Step requires Running or interrupted terminal status") {
		t.Fatalf("prepared work and committed pause both admitted: %v", err)
	}
}
