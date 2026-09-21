package oracle

import (
	"reflect"
	"strings"
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
	})
}

func TestVisitor_CollectionMembershipUsesJSONExists(t *testing.T) {
	sql, args, err := build(t, `profile['tags'] has 'rag'`)
	if err != nil {
		t.Fatal(err)
	}
	want := `json_exists(metadata, '$.profile."tags"[*]?(@ == $member)' PASSING :1 AS "member")`
	if sql != want {
		t.Fatalf("sql = %q, want %q", sql, want)
	}
	if !reflect.DeepEqual(args, []any{"rag"}) {
		t.Fatalf("args = %#v", args)
	}
}

func TestVisitor_CollectionMembershipRejectsBoolean(t *testing.T) {
	visitor := newVisitor("metadata")
	if err := filter.Has("flags", true).Accept(visitor); err == nil {
		t.Fatal("Visit() error = nil, want Oracle PASSING boolean limitation")
	}
}

// build is the test driver — parse src, visit, return (sql, args, err).
func build(t *testing.T, src string) (string, []any, error) {
	t.Helper()
	expr, err := filter.Parse(src)
	if err != nil {
		return "", nil, err
	}
	v := newVisitor("metadata")
	if err := expr.Accept(v); err != nil {
		return "", nil, err
	}
	sql, args := v.snapshot()
	return sql, args, nil
}

func TestVisitor_IsNull(t *testing.T) {
	sql, args, err := build(t, `author is null`)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(sql, "json_value(metadata, '$.author')") || !strings.Contains(sql, "IS NULL") {
		t.Fatalf("sql=%q must contain json_value(metadata, '$.author') IS NULL", sql)
	}
	if len(args) != 0 {
		t.Fatalf("IS NULL takes no bound args, got %v", args)
	}
}

func TestVisitor_IsNotNull(t *testing.T) {
	sql, _, err := build(t, `author is not null`)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// NOT(field IS NULL) — semantically IS NOT NULL.
	if !strings.Contains(sql, "NOT") || !strings.Contains(sql, "IS NULL") {
		t.Fatalf("sql=%q must wrap IS NULL in NOT", sql)
	}
}

func TestJSONPathPreservesLiteralKeysAndIndexes(t *testing.T) {
	for _, sample := range []struct{ expression, want string }{
		{`profile['a.b'] == 'keep'`, `$.profile."a.b"`},
		{`profile[':1'] == 'keep'`, `$.profile.":1"`},
		{`profile['0'] == 'keep'`, `$.profile."0"`},
		{`profile[0] == 'keep'`, `$.profile[0]`},
	} {
		predicate, err := filter.Parse(sample.expression)
		if err != nil {
			t.Fatal(err)
		}
		path, err := buildJSONPath(predicate.(*filter.BinaryExpr))
		if err != nil || path != sample.want {
			t.Fatalf("path=%q err=%v, want %q", path, err, sample.want)
		}
	}
}

func TestSearchBindsDoNotRewriteLiteralKeys(t *testing.T) {
	predicate, err := filter.Parse(`profile[':1'] == 'keep'`)
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{metadataColumn: "metadata"}
	query, args, err := store.buildFilter(predicate, 2)
	if err != nil || query != `(json_value(metadata, '$.profile.":1"') IS NOT NULL AND json_value(metadata, '$.profile.":1"') = :2)` || len(args) != 1 || args[0] != "keep" {
		t.Fatalf("query=%q args=%v error=%v", query, args, err)
	}
}
