package run_test

import (
	"encoding"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

func valid(value any) bool {
	type validator interface{ Valid() bool }
	checker, ok := value.(validator)
	if !ok {
		return false
	}
	return checker.Valid()
}

func text(value any) string {
	type stringer interface{ String() string }
	printer, ok := value.(stringer)
	if !ok {
		return ""
	}
	return printer.String()
}

// identityKind names one identity type and how to parse and re-read it, so the
// shared rules can be asserted once instead of drifting per type.
type identityKind struct {
	parse   func(string) (any, error)
	receive func() encoding.TextUnmarshaler
}

func identityKinds() map[string]identityKind {
	return map[string]identityKind{
		"process ID": {
			parse:   func(value string) (any, error) { return run.ParseProcessID(value) },
			receive: func() encoding.TextUnmarshaler { return new(run.ProcessID) },
		},
		"signal ID": {
			parse:   func(value string) (any, error) { return run.ParseSignalID(value) },
			receive: func() encoding.TextUnmarshaler { return new(run.SignalID) },
		},
		"wait ID": {
			parse:   func(value string) (any, error) { return run.ParseWaitID(value) },
			receive: func() encoding.TextUnmarshaler { return new(run.WaitID) },
		},
		"effect ID": {
			parse:   func(value string) (any, error) { return run.ParseEffectID(value) },
			receive: func() encoding.TextUnmarshaler { return new(run.EffectID) },
		},
		"wait key": {
			parse:   func(value string) (any, error) { return run.ParseWaitKey(value) },
			receive: func() encoding.TextUnmarshaler { return new(run.WaitKey) },
		},
		"child key": {
			parse:   func(value string) (any, error) { return run.ParseChildKey(value) },
			receive: func() encoding.TextUnmarshaler { return new(run.ChildKey) },
		},
	}
}

// TestIdentitiesShareOneTextContract keeps every kernel identity on the same
// parse-print-parse rule. These values key mailboxes, waits, and durable
// snapshots, so an identity that survives text differently than it was minted
// would silently address a different Process after a restore.
func TestIdentitiesShareOneTextContract(t *testing.T) {
	const sample = "01hq9y5m6t8v2x4z7a9c1e3g5j"
	for name, kind := range identityKinds() {
		t.Run(name, func(t *testing.T) {
			parsed, err := kind.parse(sample)
			if err != nil {
				t.Fatal(err)
			}
			if !valid(parsed) {
				t.Fatal("a parsed identity reports itself invalid")
			}
			if got := text(parsed); got != sample {
				t.Fatalf("identity prints %q, want %q", got, sample)
			}

			marshaler, ok := parsed.(encoding.TextMarshaler)
			if !ok {
				t.Fatal("identity does not implement encoding.TextMarshaler")
			}
			encoded, err := marshaler.MarshalText()
			if err != nil {
				t.Fatal(err)
			}
			receiver := kind.receive()
			if err := receiver.UnmarshalText(encoded); err != nil {
				t.Fatal(err)
			}
			if got := text(receiver); got != sample {
				t.Fatalf("decoded identity prints %q, want %q", got, sample)
			}
		})
	}
}

// TestIdentitiesRejectMalformedText proves the parse boundary is the only way
// in: an identity that accepted arbitrary text would let a caller forge one.
func TestIdentitiesRejectMalformedText(t *testing.T) {
	malformed := map[string]string{
		"empty":      "",
		"whitespace": "with space",
		"symbol":     "with/slash",
		"non-ASCII":  "process:节点",
		"oversized":  strings.Repeat("x", 257),
	}
	for name, kind := range identityKinds() {
		t.Run(name, func(t *testing.T) {
			for caseName, value := range malformed {
				t.Run(caseName, func(t *testing.T) {
					if _, err := kind.parse(value); err == nil {
						t.Fatalf("parse(%q) succeeded", value)
					}
					receiver := kind.receive()
					if err := receiver.UnmarshalText([]byte("valid:identity")); err != nil {
						t.Fatal(err)
					}
					if err := receiver.UnmarshalText([]byte(value)); err == nil {
						t.Fatalf("UnmarshalText(%q) succeeded", value)
					}
					if !valid(receiver) || text(receiver) != "valid:identity" {
						t.Fatal("rejected text changed the previously parsed identity")
					}
				})
			}
		})
	}
}

// TestZeroIdentitiesAreUnusable keeps an unset identity from traveling as if
// it addressed something.
func TestZeroIdentitiesAreUnusable(t *testing.T) {
	zeroes := map[string]any{
		"process ID": run.ProcessID{},
		"signal ID":  run.SignalID{},
		"wait ID":    run.WaitID{},
		"effect ID":  run.EffectID{},
		"wait key":   run.WaitKey{},
		"child key":  run.ChildKey{},
	}
	for name, zero := range zeroes {
		t.Run(name, func(t *testing.T) {
			if valid(zero) {
				t.Error("the zero identity reports itself valid")
			}
			marshaler, ok := zero.(encoding.TextMarshaler)
			if !ok {
				t.Fatal("identity does not implement encoding.TextMarshaler")
			}
			if _, err := marshaler.MarshalText(); err == nil {
				t.Error("the zero identity encoded without error")
			}
		})
	}
}

func schemaFor(t *testing.T, raw string) jsonschema.Schema {
	t.Helper()
	schema, err := jsonschema.Parse(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

// A caller must not be able to reach back into the Definition's schema.
func TestDescriptorPublishesItsCompleteContract(t *testing.T) {
	input := schemaFor(t, `{"type":"object","properties":{"question":{"type":"string"}}}`)
	output := schemaFor(t, `{"type":"object","properties":{"answer":{"type":"string"}}}`)

	descriptor, err := run.NewDescriptor(run.DescriptorConfig{
		Name:         "scope.test.answerer",
		Description:  "answers one question",
		InputSchema:  input,
		OutputSchema: output,
	})
	if err != nil {
		t.Fatal(err)
	}

	if descriptor.Name() != "scope.test.answerer" {
		t.Errorf("Name = %q", descriptor.Name())
	}
	if descriptor.Description() != "answers one question" {
		t.Errorf("Description = %q", descriptor.Description())
	}
	if !descriptor.Valid() || !descriptor.Digest().Valid() {
		t.Fatalf("descriptor valid = %t, digest valid = %t", descriptor.Valid(), descriptor.Digest().Valid())
	}

	published := descriptor.InputSchema()
	if !published.Valid() {
		t.Fatal("InputSchema is invalid")
	}
	document := published.JSON()
	if len(document) == 0 {
		t.Fatal("InputSchema published an empty document")
	}
	document[0] = 'X'
	if descriptor.InputSchema().JSON()[0] == 'X' {
		t.Fatal("InputSchema aliases the descriptor's schema")
	}
	if descriptor.OutputSchema().JSON()[0] == 'X' {
		t.Fatal("OutputSchema aliases the descriptor's schema")
	}
}

// TestDescriptorDigestFollowsTheContract is the property deployment identity
// rests on: two descriptors agree only when their whole contract agrees.
func TestDescriptorDigestFollowsTheContract(t *testing.T) {
	base := run.DescriptorConfig{
		Name:         "scope.test.answerer",
		Description:  "answers one question",
		InputSchema:  schemaFor(t, `{"type":"object"}`),
		OutputSchema: schemaFor(t, `{"type":"object"}`),
	}
	original, err := run.NewDescriptor(base)
	if err != nil {
		t.Fatal(err)
	}
	same, err := run.NewDescriptor(base)
	if err != nil {
		t.Fatal(err)
	}
	if original.Digest() != same.Digest() {
		t.Fatal("the same contract produced different digests")
	}

	changed := base
	changed.Description = "answers two questions"
	other, err := run.NewDescriptor(changed)
	if err != nil {
		t.Fatal(err)
	}
	if original.Digest() == other.Digest() {
		t.Fatal("a changed description did not change the digest")
	}
}

func TestNewDescriptorRejectsAnIncompleteContract(t *testing.T) {
	complete := run.DescriptorConfig{
		Name:         "scope.test.answerer",
		InputSchema:  schemaFor(t, `{"type":"object"}`),
		OutputSchema: schemaFor(t, `{"type":"object"}`),
	}
	cases := map[string]func(run.DescriptorConfig) run.DescriptorConfig{
		"missing name":          func(c run.DescriptorConfig) run.DescriptorConfig { c.Name = ""; return c },
		"unqualified name":      func(c run.DescriptorConfig) run.DescriptorConfig { c.Name = "Scope Test"; return c },
		"missing input schema":  func(c run.DescriptorConfig) run.DescriptorConfig { c.InputSchema = jsonschema.Schema{}; return c },
		"missing output schema": func(c run.DescriptorConfig) run.DescriptorConfig { c.OutputSchema = jsonschema.Schema{}; return c },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := run.NewDescriptor(mutate(complete)); err == nil {
				t.Fatal("NewDescriptor accepted an incomplete contract")
			}
		})
	}
}
