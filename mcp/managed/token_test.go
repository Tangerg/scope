package managed

import (
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/run"
)

func TestContinuationCapacityIsIndependentOfProcessDepth(t *testing.T) {
	signer, err := newIssuer([resumeKeyBytes]byte{7}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	root, err := run.ParseProcessID(strings.Repeat("r", 256))
	if err != nil {
		t.Fatal(err)
	}
	key, err := run.ParseChildKey(strings.Repeat("c", 256))
	if err != nil {
		t.Fatal(err)
	}
	wait, err := run.ParseWaitKey(strings.Repeat("w", 256))
	if err != nil {
		t.Fatal(err)
	}
	owner := root
	for range 256 {
		owner, err = owner.Child(key)
		if err != nil {
			t.Fatal(err)
		}
	}
	destination := target{Owner: owner, Wait: wait}
	token, err := signer.seal(destination, nil)
	if err != nil || len(token) > maximumTokenBytes {
		t.Fatalf("unreadable token: bytes=%d error=%v", len(token), err)
	}
	opened, err := signer.open(token, nil)
	if err != nil {
		t.Fatal(err)
	}
	head := run.TreeInspection{Root: root, Processes: []run.ProcessInspection{{ID: root}, {ID: owner}}}
	resolved, err := opened.resolve(t.Context(), nil, head)
	if err != nil || resolved != destination {
		t.Fatalf("target changed: %#v, %v", resolved, err)
	}
	if _, err := opened.resolve(t.Context(), nil, run.TreeInspection{}); err == nil {
		t.Fatal("token created a process outside the authoritative tree")
	}
}
