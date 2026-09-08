package redis

import (
	"context"
	"errors"
	"slices"
	"testing"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Tangerg/scope/core/history"
)

// pagedScanner answers SCAN from scripted pages and records the cursor each
// call carried, so a loop that mixes cursors between nodes is visible.
type pagedScanner struct {
	pages   [][]string
	cursors []uint64
	calls   int
	failAt  int
}

func (p *pagedScanner) Scan(ctx context.Context, cursor uint64, _ string, _ int64) *goredis.ScanCmd {
	p.cursors = append(p.cursors, cursor)
	index := p.calls
	p.calls++

	command := goredis.NewScanCmd(ctx, nil)
	if p.failAt > 0 && p.calls == p.failAt {
		command.SetErr(errors.New("scan failed"))
		return command
	}
	if index >= len(p.pages) {
		command.SetErr(errors.New("unexpected SCAN call"))
		return command
	}
	next := uint64(0)
	if index+1 < len(p.pages) {
		// A real cursor is opaque and node-local; any non-zero value ends the
		// page without ending the scan.
		next = uint64(index+1) * 17
	}
	command.SetVal(p.pages[index], next)
	return command
}

func newCollector() *conversationCollector {
	return &conversationCollector{
		keyPrefix: DefaultKeyPrefix,
		match:     DefaultKeyPrefix + "*",
		seen:      make(map[string]struct{}),
	}
}

// A cursor is only meaningful to the node that issued it, so one node's scan
// has to run from 0 to 0 without another node's cursor in between.
func TestScanDrivesOneNodeCursorToCompletion(t *testing.T) {
	t.Parallel()

	scanner := &pagedScanner{pages: [][]string{
		{DefaultKeyPrefix + "alpha"},
		{},
		{DefaultKeyPrefix + "beta", DefaultKeyPrefix + "alpha"},
	}}

	collector := newCollector()
	if err := collector.scan(t.Context(), scanner); err != nil {
		t.Fatalf("scan() = %v, want nil", err)
	}

	if want := []uint64{0, 17, 34}; !slices.Equal(scanner.cursors, want) {
		t.Fatalf("cursors = %v, want %v", scanner.cursors, want)
	}
	// The empty middle page is not the end of the scan; a loop that stopped
	// there would drop beta.
	if got := collector.sorted(); !slices.Equal(got, []history.ConversationID{"alpha", "beta"}) {
		t.Fatalf("sorted() = %v, want [alpha beta]", got)
	}
}

// Every node contributes to one result, and a key seen twice -- SCAN may repeat
// under concurrent mutation -- appears once.
func TestCollectorMergesNodesAndDeduplicates(t *testing.T) {
	t.Parallel()

	collector := newCollector()
	for _, keys := range [][]string{
		{DefaultKeyPrefix + "gamma", DefaultKeyPrefix + "alpha"},
		{DefaultKeyPrefix + "beta", DefaultKeyPrefix + "gamma"},
	} {
		if err := collector.scan(t.Context(), &pagedScanner{pages: [][]string{keys}}); err != nil {
			t.Fatalf("scan() = %v, want nil", err)
		}
	}

	if got := collector.sorted(); !slices.Equal(got, []history.ConversationID{"alpha", "beta", "gamma"}) {
		t.Fatalf("sorted() = %v, want [alpha beta gamma]", got)
	}
}

// A node that fails mid-scan is reported. Returning what was gathered so far
// would present a partial keyspace as the whole one.
func TestScanReportsANodeFailure(t *testing.T) {
	t.Parallel()

	scanner := &pagedScanner{
		pages:  [][]string{{DefaultKeyPrefix + "alpha"}, {DefaultKeyPrefix + "beta"}},
		failAt: 2,
	}

	if err := newCollector().scan(t.Context(), scanner); err == nil {
		t.Fatal("scan() = nil error, want the node failure reported")
	}
}

// An empty store answers with a non-nil empty slice, which Lister requires.
func TestSortedIsNeverNil(t *testing.T) {
	t.Parallel()

	if got := newCollector().sorted(); got == nil {
		t.Fatal("sorted() = nil, want a non-nil empty slice")
	}
}

// A ring shards the keyspace and SCAN carries no key, so the enumeration has to
// go through ForEachShard rather than the client. A ring with no shards proves
// which path runs: ForEachShard visits nothing and succeeds, while a SCAN sent
// to the ring itself fails because no shard can serve it.
func TestConversationsEnumeratesARingPerShard(t *testing.T) {
	t.Parallel()

	ring := goredis.NewRing(&goredis.RingOptions{Addrs: map[string]string{}})
	t.Cleanup(func() { _ = ring.Close() })

	store, err := NewStore(StoreConfig{Client: ring})
	if err != nil {
		t.Fatalf("NewStore() = %v, want nil", err)
	}
	ids, err := store.Conversations(t.Context())
	if err != nil {
		t.Fatalf("Conversations() = %v, want nil", err)
	}
	if len(ids) != 0 || ids == nil {
		t.Fatalf("Conversations() = %v, want a non-nil empty slice", ids)
	}
}
