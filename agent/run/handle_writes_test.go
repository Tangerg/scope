package run

import (
	"testing"
)

// Hosts act through a Handle only while its writer committed the head they
// read; a recovered tree or a closed writer must not look writable.
func TestHandleWritesOnlyTheHeadItsRunningWriterCommitted(t *testing.T) {
	store := NewMemoryStore()
	first := kernelRuntime(t, Config{Store: store})
	handle := controlValue(first.Start(t.Context(), treeFixtureProgram(t, first, 0, true, false, Budget{}), StartRequest[int]{ID: hostStartID()}))
	head := controlValue(first.Inspect(t.Context(), handle.ID()))
	if !handle.Writes(head) {
		t.Fatal("the starting writer does not write its own head")
	}
	foreign := head
	foreign.Root = hostStartID().Root()
	if handle.Writes(foreign) {
		t.Fatal("a Handle writes another tree")
	}
	if err := first.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if handle.Writes(head) {
		t.Fatal("a closed writer still writes its head")
	}

	second := kernelRuntime(t, Config{Store: store})
	recovered := controlValue(second.Recover(t.Context(), treeFixtureProgram(t, second, 0, true, false, Budget{}), handle.ID()))
	current := controlValue(second.Inspect(t.Context(), handle.ID()))
	if !recovered.Writes(current) || handle.Writes(current) {
		t.Fatal("writer ownership did not move to the recovering Runtime")
	}
}
