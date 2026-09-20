package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
)

func TestOversizedSignalBatchLeavesDurableTreeUsable(t *testing.T) {
	store := &recordingTreeCommitter{}
	engine, err := NewEngine(EngineConfig{TreeCommitter: store, Limits: Limits{MaxSnapshotBytes: NewQuota(128 << 14)}})
	if err != nil {
		t.Fatal(err)
	}
	defer mustCloseEngine(t, engine)
	deployment := newChildTestDeployment(t)
	input := controlValue(EncodePayload(childTestInput{Mode: "leaf_pause"}))
	process, err := engine.Start(t.Context(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, process, StatusPaused)
	checkpoints := store.treeCheckpoints()
	before := checkpoints[len(checkpoints)-1].TreeSnapshot()
	payload := []byte(`"` + strings.Repeat("x", 45<<14) + `"`)
	var requests []SignalRequest
	for index := range 3 {
		requests = append(requests, controlValue(NewSignalRequest(controlValue(ParseSignalID(fmt.Sprintf("signal:large-%d", index))), WaitID{}, payload)))
	}
	accepted, err := process.DeliverSignals(t.Context(), requests...)
	if accepted || !errors.Is(err, ErrResourceLimitExceeded) {
		t.Fatalf("oversized admission = %t, %v", accepted, err)
	}
	checkpoints = store.treeCheckpoints()
	after := checkpoints[len(checkpoints)-1].TreeSnapshot()
	if !bytes.Equal(before.JSON(), after.JSON()) {
		t.Fatal("rejection changed durable head")
	}
	snapshot := inspectProcessSnapshot(t, process)
	if snapshot.Status() != StatusPaused || snapshot.Usage().AcceptedSignals != 0 || len(snapshot.SignalReceipts()) != 0 {
		t.Fatal("rejection changed process facts")
	}
	small := controlValue(NewSignalRequest(controlValue(ParseSignalID("signal:small")), WaitID{}, []byte(`"accepted"`)))
	accepted, err = process.DeliverSignals(t.Context(), small)
	if err != nil || !accepted {
		t.Fatalf("legal admission after rejection = %t, %v", accepted, err)
	}
	if snapshot := inspectProcessSnapshot(t, process); snapshot.Status() != StatusPaused || snapshot.Usage().AcceptedSignals != 1 {
		t.Fatal("tree stopped after capacity refusal")
	}
	if err := process.Kill(t.Context(), "test complete"); err != nil {
		t.Fatal(err)
	}
	mustAwait(t, process)
}

func TestTreeAdmissionSizeMatchesPersistedEncoding(t *testing.T) {
	runtime := newWaitingSnapshotTree(t, 3)
	header, err := json.Marshal(runtime.treeSnapshotBase())
	if err != nil {
		t.Fatal(err)
	}
	size := len(header)
	for index, process := range slices.Collect(maps.Values(runtime.processes)) {
		data, encodeErr := json.Marshal(process.snapshotWire())
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		size += len(data)
		if index > 0 {
			size++
		}
	}
	snapshot, err := runtime.captureTree()
	if err != nil {
		t.Fatal(err)
	}
	if size != len(snapshot.JSON()) {
		t.Fatalf("admission size=%d persisted size=%d", size, len(snapshot.JSON()))
	}
}
