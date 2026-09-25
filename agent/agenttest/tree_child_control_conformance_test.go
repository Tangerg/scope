package agenttest

import "testing"

// Without this check a reference Effect that stopped building would only
// surface later, as every control scenario timing out waiting for a commit it
// can no longer recognize.
func TestChildControlReferencesNameTwoDistinctOperations(t *testing.T) {
	t.Parallel()
	references := childControlReferences()
	names := make(map[childControlOperation]string, len(references))
	for name, operation := range references {
		if name == "" {
			t.Errorf("%s has no framework operation name", operation)
		}
		names[operation] = name
	}
	if len(names) != 2 || names[childControlSignal] == "" || names[childControlCancel] == "" {
		t.Fatalf("child control references = %v, want one distinct name per operation", references)
	}
}
