package run

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type effectBenchmarkDurability struct{ Store }

func (e effectBenchmarkDurability) CommitEffect(context.Context, EffectBoundary) error { return nil }

func BenchmarkEffectBoundaryCommit(b *testing.B) {
	for _, count := range []int{1, 100, 1000} {
		for _, size := range []int{1024, 64 << 10} {
			b.Run(fmt.Sprintf("processes_%d/bytes_%d", count, size), func(b *testing.B) {
				runtime, request, snapshot := effectBoundaryFixture(b, count, size)
				previous := runtime.writer.head()
				b.ReportAllocs()
				for b.Loop() {
					runtime.writer.establish(previous)
					commit := &treeCommit{kind: treeCommitEffectPending, processID: request.Relation().ProcessID(), effectID: request.ID(), snapshot: snapshot}
					if err := runtime.writer.commitEffect(b.Context(), commit); err != nil {
						b.Fatal(err)
					}
					completed := <-runtime.writer.done
					if _, current := runtime.writer.settle(completed); !current || completed.err != nil {
						b.Fatal(completed.err)
					}
				}
			})
		}
	}
}

func effectBoundaryFixture(t testing.TB, count, size int) (*treeRuntime, EffectRequest, TreeSnapshot) {
	t.Helper()
	runtime := newWaitingSnapshotTree(t, count)
	runtime.writer.committer = effectBenchmarkDurability{}
	runtime.writer.identity = newTreeIncarnationID()
	root := runtime.members.get(runtime.rootID)
	previous, err := runtime.captureTree()
	if err != nil {
		t.Fatal(err)
	}
	runtime.writer.establish(previous)
	effect, err := NewDispatcherEffect([]byte(`{"text":"` + strings.Repeat("x", size) + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	transition, err := Continue(0, effect)
	if err != nil {
		t.Fatal(err)
	}
	if failure := prepareTestStep(root, runtime.treeLimits, stepJobResult{transition: transition, candidateState: root.committedExecutionState}); failure != nil {
		t.Fatal(failure.cause)
	}
	root.prepared.Effects[0].progress = &effectProgress{}
	snapshot, err := runtime.captureTree()
	if err != nil {
		t.Fatal(err)
	}
	return runtime, runtime.effectRequestFor(root, 0, root.prepared.Effects[0]), snapshot
}

func TestEffectBoundaryDerivesItsEffectFromTheProspectiveTree(t *testing.T) {
	runtime, request, snapshot := effectBoundaryFixture(t, 3, 64)
	previous := runtime.writer.head()
	boundary, err := newEffectBoundary(1, false, request.Relation().ProcessID(), request.ID(), previous, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if boundary.Kind() != EffectBoundaryKindPending || boundary.Request().ID() != request.ID() ||
		!boundary.Request().Effect().equal(request.Effect()) {
		t.Fatalf("boundary = %s %+v, want the captured pending request", boundary.Kind(), boundary.Request())
	}
	if _, settled := boundary.Settlement(); settled {
		t.Fatal("pending boundary reported a settlement")
	}
	if _, err := newEffectBoundary(1, false, request.Relation().ProcessID(), request.Relation().ProcessID().effectID(9, 0), previous, snapshot); err == nil {
		t.Fatal("boundary for an uncaptured Effect was admitted")
	}
	if _, err := newEffectBoundary(1, true, request.Relation().ProcessID(), request.ID(), previous, snapshot); err == nil {
		t.Fatal("pending Effect was admitted as a resolution")
	}
	if (EffectBoundary{}).Valid() {
		t.Fatal("zero boundary is valid")
	}
}

func BenchmarkChangedProcessRecords(b *testing.B) {
	for _, members := range []int{1, 17, 63} {
		b.Run(fmt.Sprintf("members_%d", members), func(b *testing.B) {
			runtime, request, snapshot := effectBoundaryFixture(b, members, 64)
			update := controlValue(newEffectBoundary(1, false, request.Relation().ProcessID(), request.ID(), runtime.writer.head(), snapshot))
			b.ReportAllocs()
			var records, size int
			for b.Loop() {
				records, size = 0, 0
				for _, member := range update.ChangedProcesses() {
					records++
					size += len(member.data)
				}
			}
			b.ReportMetric(float64(records), "changed_records")
			b.ReportMetric(float64(size), "changed_bytes")
			b.ReportMetric(float64(snapshot.EncodedSize()), "tree_bytes")
		})
	}
}

func TestCommitProjectsOnlyChangedMembersFromItsComparedHead(t *testing.T) {
	runtime, request, tree := effectBoundaryFixture(t, 17, 64)
	// An adapter may receive a head exported by a different process.
	before := controlValue(ParseTreeSnapshot(runtime.writer.head().JSON()))
	boundary := controlValue(newEffectBoundary(1, false, request.Relation().ProcessID(), request.ID(), before, tree))
	if boundary.PreviousTreeDigest() != before.Digest() {
		t.Fatal("wrong predecessor")
	}
	count := 0
	for id, member := range boundary.ChangedProcesses() {
		count++
		if id != tree.RootID() || member.Digest() != tree.state.processSnapshot(id).Digest() {
			t.Fatal("unmodified or incorrect Process", id)
		}
	}
	if count != 1 {
		t.Fatalf("changed members = %d, want one", count)
	}
}
