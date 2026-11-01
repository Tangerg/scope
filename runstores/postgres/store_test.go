package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Tangerg/scope/agent/run"
)

func TestAbsentConfigurationAndInvalidKeysDoNotUseThePool(t *testing.T) {
	if got, err := New(t.Context(), Config{}); got != nil || err == nil {
		t.Fatal("accepted empty configuration")
	}
	var s Store
	if _, _, err := s.LookupStart(t.Context(), run.StartID{}); !errors.Is(err, run.ErrInvalidStart) {
		t.Fatal(err)
	}
	if _, _, err := s.LoadTree(t.Context(), run.ProcessID{}); !errors.Is(err, run.ErrInvalidTreeSnapshot) {
		t.Fatal(err)
	}
	if err := s.ActivateTree(t.Context(), run.TreeActivation{}); err == nil {
		t.Fatal("invalid activation")
	}
	if err := s.CommitEffect(t.Context(), run.EffectBoundary{}); err == nil {
		t.Fatal("invalid effect")
	}
	if err := s.CommitCheckpoint(t.Context(), run.TreeCheckpoint{}); err == nil {
		t.Fatal("invalid checkpoint")
	}
}
func TestSchemaNamesAreOneQuotedIdentifier(t *testing.T) {
	for _, bad := range []string{"", strings.Repeat("a", 64), "a\x00b", string([]byte{0xff})} {
		if _, err := SchemaSQL(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	ddl, err := SchemaSQL(`t";DROP SCHEMA other;--`)
	if err != nil || !strings.Contains(ddl, `"t"";DROP SCHEMA other;--".tree_heads`) {
		t.Fatalf("unsafe DDL: %s (%v)", ddl, err)
	}
}

type receiptRow struct {
	intent, root string
	err          error
}

func (r receiptRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*dest[0].(*string) = r.intent
	*dest[1].(*string) = r.root
	return nil
}
func (r receiptRow) QueryRow(context.Context, string, ...any) pgx.Row { return r }

func TestLookupNeverApprovesMissingOrCorruptReceipt(t *testing.T) {
	id := run.StartID{Scope: "tenant", Key: "one"}
	s := Store{tables: newRelations(`"scope"`)}
	for _, r := range []receiptRow{{err: pgx.ErrNoRows}, {err: context.Canceled}, {intent: "bad", root: "bad"}, {intent: run.ComputeDigest([]byte("input")).String(), root: ""}} {
		got, ok, err := s.lookupStart(t.Context(), r, id)
		if ok || got.Valid() {
			t.Fatalf("accepted corrupt receipt: %+v", got)
		}
		if errors.Is(r.err, pgx.ErrNoRows) {
			if err != nil {
				t.Fatal(err)
			}
		} else if err == nil || r.err != nil && !errors.Is(err, r.err) {
			t.Fatalf("lost storage error: %v", err)
		}
	}
}
func TestCloseCancelsCurrentAndFutureOperations(t *testing.T) {
	lifetime, stop := context.WithCancel(context.Background())
	s := Store{shutdown: lifetime, stop: stop, timeout: time.Minute}
	first, done := s.operation(t.Context())
	defer done()
	s.Close()
	select {
	case <-first.Done():
	case <-time.After(time.Second):
		t.Fatal("Close left operation live")
	}
	next, done2 := s.operation(t.Context())
	defer done2()
	if !errors.Is(next.Err(), context.Canceled) {
		t.Fatal(next.Err())
	}
}
