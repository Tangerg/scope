package run

import (
	"errors"
	"testing"
)

func TestObservationCursorCoversSnapshotAttachmentAndSlowReader(t *testing.T) {
	first, _, activation := treeCommitIdentityFixture(t)
	old, restored := first.TreeSnapshot(), activation.TreeSnapshot()
	var stream treeStream
	stream.acknowledged(old)
	initial, slow, err := stream.attach(Cursor{}, 1)
	if err != nil || initial.Snapshot.Digest() != old.Digest() {
		t.Fatal(initial, err)
	}
	_, fast, err := stream.attach(initial.Cursor, 1)
	if err != nil {
		t.Fatal(err)
	}
	stream.acknowledged(old)
	update := <-fast.queue
	if update.Cursor == initial.Cursor || update.Snapshot.Digest() != old.Digest() {
		t.Fatal("acknowledgment lacks a fresh cursor")
	}
	stream.acknowledged(old)
	if next := <-fast.queue; next.Cursor.Position != update.Cursor.Position+1 {
		t.Fatal("fast observer lost an update")
	}
	<-slow.queue
	if _, ok := <-slow.queue; ok || !slow.gap {
		t.Fatal("overflow was silent")
	}
	if _, _, attachErr := stream.attach(initial.Cursor, 1); !errors.Is(attachErr, ErrObservationGap) {
		t.Fatal("stale cursor resumed")
	}
	view, resynced, err := stream.attach(Cursor{}, 1)
	if err != nil || view.Cursor.RootID != old.RootID() || view.Cursor.IncarnationID != old.IncarnationID() {
		t.Fatal(view, err)
	}
	stream.finish()
	if _, ok := <-resynced.queue; ok || resynced.gap {
		t.Fatal("clean completion became a gap")
	}
	var recovered treeStream
	recovered.acknowledged(restored)
	if _, _, err := recovered.attach(view.Cursor, 1); !errors.Is(err, ErrObservationGap) {
		t.Fatal("cursor crossed a writer activation")
	}
}
