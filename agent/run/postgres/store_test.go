package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Tangerg/scope/agent/run"
)

func TestInvalidConfigurationAndAddressesDoNotUseThePool(t *testing.T) {
	for _, config := range []Config{
		{},
		{Pool: &pgxpool.Pool{}, Schema: "invalid\x00schema", Timeout: time.Second},
	} {
		if got, err := New(t.Context(), config); got != nil || err == nil {
			t.Fatal("invalid configuration reached the pool")
		}
	}
	var store Store
	child, err := run.ParseProcessID("root/child")
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range []run.ProcessID{{}, child} {
		if _, _, err := store.Load(t.Context(), root); err == nil {
			t.Fatal("accepted a non-root log address")
		}
	}
	if err := store.Append(t.Context(), run.Batch{}); err == nil {
		t.Fatal("accepted invalid append")
	}
}

func TestSchemaNamesAreOneQuotedIdentifier(t *testing.T) {
	for _, bad := range []string{"", strings.Repeat("a", maxIdentifierBytes+1), "a\x00b", string([]byte{0xff})} {
		if _, err := SchemaSQL(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	ddl, err := SchemaSQL(`t";DROP SCHEMA other;--`)
	if err != nil || !strings.Contains(ddl, `"t"";DROP SCHEMA other;--".heads`) || !strings.Contains(ddl, `"t"";DROP SCHEMA other;--".log`) {
		t.Fatalf("unsafe DDL: %s (%v)", ddl, err)
	}
}

func TestCorruptCoordinatesAndRecordKindsAreRejected(t *testing.T) {
	for _, text := range []string{"", "-1", "18446744073709551616", "1.5"} {
		if _, err := parsePosition[run.LSN](text); err == nil {
			t.Fatalf("accepted log position %q", text)
		}
	}
	for _, input := range []struct{ lsn, epoch, kind string }{
		{"-1", "1", "activated"},
		{"1", "-1", "activated"},
		{"1", "1", "unknown"},
	} {
		if _, err := decodeEntry(input.lsn, input.epoch, input.kind, []byte(`{"epoch":1}`)); err == nil {
			t.Fatalf("accepted corrupt entry %+v", input)
		}
	}
}

func TestRecordCoordinatesPreserveTheirCompleteUnsignedRange(t *testing.T) {
	const largest = "18446744073709551615"
	entry, err := decodeEntry(largest, largest, string(run.RecordKindActivated), []byte(`{"epoch":`+largest+`}`))
	if err != nil {
		t.Fatal(err)
	}
	activation, ok := entry.Record.(run.Activated)
	if !ok || entry.LSN != ^run.LSN(0) || entry.Epoch != ^run.Epoch(0) || activation.Epoch != entry.Epoch {
		t.Fatalf("numeric storage narrowed a legal unsigned coordinate: %+v", entry)
	}
}

func TestCloseCancelsCurrentAndFutureOperations(t *testing.T) {
	lifetime, stop := context.WithCancel(t.Context())
	store := Store{shutdown: lifetime, stop: stop, timeout: time.Minute}
	first, done := store.operation(t.Context())
	defer done()
	store.Close()
	select {
	case <-first.Done():
	case <-time.After(time.Second):
		t.Fatal("Close left operation live")
	}
	next, finish := store.operation(t.Context())
	defer finish()
	if !errors.Is(next.Err(), context.Canceled) {
		t.Fatal(next.Err())
	}
}
