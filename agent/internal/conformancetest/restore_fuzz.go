package conformancetest

import (
	"testing"

	"github.com/Tangerg/scope/agent/run"
)

// FuzzRestoreRoundTrip requires every state Restore admits to survive its own
// Snapshot. A member absent from a record never reaches its type's decoder, so
// an admitted state that snapshots into one Restore refuses exposes a check the
// Strategy left to a decoder that never ran.
func FuzzRestoreRoundTrip(f *testing.F, definition run.Definition, kind string, seeds ...run.ExecutionState) {
	f.Helper()
	for _, seed := range seeds {
		f.Add([]byte(seed.Payload().JSON()))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		payload, err := run.ParsePayload(data)
		if err != nil {
			return
		}
		state, err := run.NewExecutionState(kind, payload)
		if err != nil {
			return
		}
		execution, err := definition.Restore(t.Context(), state)
		if err != nil {
			return
		}
		first, err := execution.Snapshot()
		if err != nil {
			t.Fatalf("admitted state cannot snapshot: %v", err)
		}
		restored, err := definition.Restore(t.Context(), first)
		if err != nil {
			t.Fatalf("admitted state does not restore from its own snapshot: %v\nrecord: %s", err, data)
		}
		second, err := restored.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if !first.Payload().Equal(second.Payload()) {
			t.Fatalf("snapshot changed across Restore\nfirst: %s\nsecond: %s", first.Payload().JSON(), second.Payload().JSON())
		}
	})
}
