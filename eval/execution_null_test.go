package eval

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"
)

type nullableCandidate struct {
	Present bool
	Value   string
}

func (n nullableCandidate) MarshalJSON() ([]byte, error) {
	if !n.Present {
		return []byte("null"), nil
	}
	return jsonv2.Marshal(n.Value)
}

func (n *nullableCandidate) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		*n = nullableCandidate{}
		return nil
	}
	var value string
	if err := jsonv2.Unmarshal(data, &value); err != nil {
		return err
	}
	*n = nullableCandidate{Present: true, Value: value}
	return nil
}

func TestExecutionRejectsNullThatWouldInventAZeroValue(t *testing.T) {
	t.Run("string", func(t *testing.T) { rejectNullCandidate(t, "kept") })
	t.Run("integer", func(t *testing.T) { rejectNullCandidate(t, 42) })
	t.Run("boolean", func(t *testing.T) { rejectNullCandidate(t, true) })
	t.Run("object", func(t *testing.T) { rejectNullCandidate(t, struct{ Text string }{Text: "kept"}) })
	t.Run("slice codec", func(t *testing.T) { rejectNullCandidate(t, []string{"kept"}) })
	t.Run("map codec", func(t *testing.T) { rejectNullCandidate(t, map[string]string{"text": "kept"}) })
}

func rejectNullCandidate[O any](t *testing.T, value O) {
	t.Helper()
	execution := Execution[O]{Status: ExecutionFailed, Output: &value, Reason: "preserve this receipt"}
	before, err := jsonv2.Marshal(execution)
	if err != nil {
		t.Fatal(err)
	}
	err = jsonv2.Unmarshal([]byte(`{"status":"completed","output":null}`), &execution)
	if !errors.Is(err, ErrInvalidExecution) {
		t.Fatalf("null became a candidate: %#v, %v", execution, err)
	}
	after, err := jsonv2.Marshal(execution)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("failed decode replaced the receipt: %s -> %s, %v", before, after, err)
	}
}

func TestExecutionPreservesCustomNullableCandidate(t *testing.T) {
	var execution Execution[nullableCandidate]
	if err := jsonv2.Unmarshal([]byte(`{"status":"completed","output":null}`), &execution); err != nil {
		t.Fatal(err)
	}
	if execution.Output == nil || execution.Output.Present {
		t.Fatalf("null candidate lost its presence: %#v", execution)
	}
	encoded, err := jsonv2.Marshal(execution)
	if err != nil || string(encoded) != `{"status":"completed","output":null}` {
		t.Fatalf("null did not round trip: %s, %v", encoded, err)
	}
	if err = jsonv2.Unmarshal([]byte(`{"status":"completed"}`), &execution); err != nil || execution.Output != nil {
		t.Fatalf("absent candidate gained presence: %#v, %v", execution, err)
	}
}
