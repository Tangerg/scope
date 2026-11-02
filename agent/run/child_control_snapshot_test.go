package run_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tangerg/scope/agent/run"
)

func TestParseTreeSnapshotRejectsSuccessfulControlOfMissingChild(t *testing.T) {
	for _, operation := range []string{"signal", "cancel"} {
		t.Run(operation, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "absent-child-"+operation+".json"))
			if err != nil {
				t.Fatal(err)
			}
			if snapshot, err := run.ParseTreeSnapshot(data); snapshot.Valid() || !errors.Is(err, run.ErrInvalidTreeSnapshot) || !errors.Is(err, run.ErrInvalidChildControl) {
				t.Fatalf("missing child accepted: valid=%t error=%v", snapshot.Valid(), err)
			}
		})
	}
}
