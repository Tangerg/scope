package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type effectBenchmarkDurability struct{ TreeCommitter }

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
					commit := &treeCommit{processID: request.ProcessID(), snapshot: snapshot}
					if err := runtime.writer.commitEffect(b.Context(), commit, EffectBoundaryKindPending, request, Settlement{}); err != nil {
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
	root.prepared.Effects[0].Phase = effectPhasePending
	snapshot, err := runtime.captureTree()
	if err != nil {
		t.Fatal(err)
	}
	return runtime, runtime.effectRequestFor(root, 0, root.prepared.Effects[0]), snapshot
}

func TestEffectBoundaryConstructionRejectsMismatchedEffect(t *testing.T) {
	_, request, snapshot := effectBoundaryFixture(t, 3, 64)
	previous := ComputeDigest([]byte("previous tree"))
	if _, err := newEffectBoundary(1, EffectBoundaryKindPending, request, Settlement{}, previous, snapshot); err != nil {
		t.Fatal(err)
	}
	var err error
	request.effect, err = NewDispatcherEffect([]byte(`{"different":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newEffectBoundary(1, EffectBoundaryKindPending, request, Settlement{}, previous, snapshot); err == nil {
		t.Fatal("mismatched effect was admitted")
	}
	if (EffectBoundary{}).Valid() {
		t.Fatal("zero boundary is valid")
	}
}
