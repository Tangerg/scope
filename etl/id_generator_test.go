package etl_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/etl"
)

func newDocument(t *testing.T, text string) *document.Document {
	t.Helper()
	doc, err := document.NewDocument(text, nil)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestSHA256GeneratorDeterministic(t *testing.T) {
	generator := etl.NewSHA256IDGenerator(nil)
	doc := newDocument(t, "hello")
	if err := doc.Metadata.Set("position", 42); err != nil {
		t.Fatal(err)
	}

	first, err := generator.Generate(t.Context(), doc)
	if err != nil {
		t.Fatal(err)
	}
	second, err := generator.Generate(t.Context(), doc)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("same document produced different digests:\n  %s\n  %s", first, second)
	}
}

func TestSHA256IdentityIgnoresJSONPresentationWithoutRoundingNumbers(t *testing.T) {
	generator := etl.NewSHA256IDGenerator(nil)
	identity := func(raw string) string {
		t.Helper()
		doc := newDocument(t, "same")
		doc.Metadata["nested"] = json.RawMessage(raw)
		id, err := generator.Generate(t.Context(), doc)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	baseline := identity(`{"a":[{"x":"<","y":9007199254740993}],"b":1.00000000000000001}`)
	for _, test := range []struct {
		name, raw string
		equal     bool
	}{
		{name: "presentation", raw: ` { "b":1.00000000000000001, "a":[{"y":9007199254740993,"x":"\u003c"}] } `, equal: true},
		{name: "integer precision", raw: `{"a":[{"x":"<","y":9007199254740992}],"b":1.00000000000000001}`},
		{name: "decimal precision", raw: `{"a":[{"x":"<","y":9007199254740993}],"b":1.00000000000000002}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if equal := identity(test.raw) == baseline; equal != test.equal {
				t.Fatalf("same identity = %t, want %t", equal, test.equal)
			}
		})
	}
}

func TestSHA256GeneratorUsesDocumentContentButNotExistingID(t *testing.T) {
	generator := etl.NewSHA256IDGenerator(nil)
	first := newDocument(t, "hello")
	second := newDocument(t, "world")

	digestA, err := generator.Generate(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	digestB, err := generator.Generate(t.Context(), second)
	if err != nil {
		t.Fatal(err)
	}
	if digestA == digestB {
		t.Fatal("different document content produced the same digest")
	}

	first.ID = "already-assigned"
	again, err := generator.Generate(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	if again != digestA {
		t.Fatal("an existing ID changed the content digest")
	}
}

func TestSHA256GeneratorSaltSeparatesStreams(t *testing.T) {
	doc := newDocument(t, "doc")
	plain, err := etl.NewSHA256IDGenerator(nil).Generate(t.Context(), doc)
	if err != nil {
		t.Fatal(err)
	}
	salted, err := etl.NewSHA256IDGenerator([]byte("tenant-A")).Generate(t.Context(), doc)
	if err != nil {
		t.Fatal(err)
	}
	otherSalt, err := etl.NewSHA256IDGenerator([]byte("tenant-B")).Generate(t.Context(), doc)
	if err != nil {
		t.Fatal(err)
	}
	if plain == salted || salted == otherSalt {
		t.Fatal("salt must change the digest stream")
	}
	if len(plain) != 64 || len(salted) != 64 || len(otherSalt) != 64 {
		t.Fatal("SHA-256 identifiers must contain 64 hex characters")
	}
}

func TestSHA256GeneratorRejectsInvalidDocument(t *testing.T) {
	doc := newDocument(t, "doc")
	doc.Metadata["invalid"] = json.RawMessage(`{`)
	if _, err := etl.NewSHA256IDGenerator(nil).Generate(t.Context(), doc); err == nil {
		t.Fatal("invalid metadata produced an ID")
	}
	if _, err := etl.NewSHA256IDGenerator(nil).Generate(t.Context(), nil); !errors.Is(err, etl.ErrNilDocument) {
		t.Fatalf("nil document error = %v, want ErrNilDocument", err)
	}
}

func TestUUIDGeneratorRejectsInvalidDocument(t *testing.T) {
	doc := newDocument(t, "doc")
	doc.Metadata["invalid"] = json.RawMessage(`{`)
	if _, err := (etl.UUIDGenerator{}).Generate(t.Context(), doc); err == nil {
		t.Fatal("invalid document produced a UUID")
	}
}

func TestSHA256GeneratorCopiesSalt(t *testing.T) {
	salt := []byte("tenant-A")
	generator := etl.NewSHA256IDGenerator(salt)
	salt[0] = 'X'
	doc := newDocument(t, "doc")

	got, err := generator.Generate(t.Context(), doc)
	if err != nil {
		t.Fatal(err)
	}
	want, err := etl.NewSHA256IDGenerator([]byte("tenant-A")).Generate(t.Context(), doc)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatal("generator retained caller-owned salt storage")
	}
}

func TestGeneratorsHonorCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	doc := newDocument(t, "doc")
	for _, generator := range []etl.IDGenerator{etl.NewSHA256IDGenerator(nil), etl.UUIDGenerator{}} {
		if _, err := generator.Generate(ctx, doc); !errors.Is(err, context.Canceled) {
			t.Fatalf("Generate() error = %v, want context.Canceled", err)
		}
	}
}

func TestUUIDGeneratorReturnsUniqueIDs(t *testing.T) {
	generator := etl.UUIDGenerator{}
	doc := newDocument(t, "same input")
	first, err := generator.Generate(t.Context(), doc)
	if err != nil {
		t.Fatal(err)
	}
	second, err := generator.Generate(t.Context(), doc)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("UUID generator returned identical IDs")
	}
}
