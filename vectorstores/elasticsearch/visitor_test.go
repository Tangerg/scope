package elasticsearch

import (
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

func TestVisitor_Conformance(t *testing.T) {
	storetest.VisitorConformance(t, func(src string) error {
		expr, err := filter.Parse(src)
		if err != nil {
			return err
		}
		v := newVisitor("metadata")
		return expr.Accept(v)
	},
		storetest.Options{
			// This compiler writes a metadata key into the query language as text,
			// so a key that language cannot name is refused rather than approximated.
			InterpolatesKeyPaths: true,
		},
	)
}

func TestVisitor_NullTest(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			// A field is null when it is absent: negate the existence check.
			name: "is null",
			src:  "author is null",
			want: "NOT _exists_:metadata.author",
		},
		{
			// IS NOT NULL arrives as NOT(field IS NULL); the NOT wrapper
			// double-negates the existence check, leaving a plain exists.
			name: "is not null",
			src:  "author is not null",
			want: "NOT (NOT _exists_:metadata.author)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expr, err := filter.Parse(tt.src)
			if err != nil {
				t.Fatalf("parse %q: %v", tt.src, err)
			}
			v := newVisitor("metadata")
			if err := expr.Accept(v); err != nil {
				t.Fatalf("visit %q: %v", tt.src, err)
			}
			if got := v.snapshot(); got != tt.want {
				t.Errorf("Result() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestVisitor_CollectionMembershipUsesExactFieldQuery(t *testing.T) {
	expr := filter.Has("visible_to", "user-42")
	visitor := newVisitor("metadata")
	if err := expr.Accept(visitor); err != nil {
		t.Fatal(err)
	}
	if got, want := visitor.snapshot(), `metadata.visible_to:"user-42"`; got != want {
		t.Fatalf("Result() = %q, want %q", got, want)
	}
}
