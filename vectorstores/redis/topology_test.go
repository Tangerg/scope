package redis

import (
	"context"
	"errors"
	"testing"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestCompleteScanCapabilityIsRequired(t *testing.T) {
	fixture := newProtocolFixture(t)
	store, err := NewStore(t.Context(), StoreConfig{Client: fixture.client, Space: fixtureSpace})
	if err == nil || store != nil {
		t.Fatalf("missing traversal accepted: %v, %v", store, err)
	}
}

func TestNodeFailurePreventsPartialDeletion(t *testing.T) {
	fixture := newProtocolFixture(t)
	store := fixture.store(1)
	if err := store.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "one", Text: "text"}})); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("second master unavailable")
	store.scanNodes = func(ctx context.Context, visit func(context.Context, *goredis.Client) error) error {
		if err := visit(ctx, fixture.client); err != nil {
			return err
		}
		return cause
	}
	if err := store.DeleteMatching(t.Context(), filter.IsNull("absent"), fixtureBudget()); !errors.Is(err, cause) {
		t.Fatalf("incomplete traversal error = %v", err)
	}
	if len(fixture.rows) != 1 || fixture.deleted != 0 {
		t.Fatalf("incomplete traversal deleted records: %d rows, %d deletions", len(fixture.rows), fixture.deleted)
	}
}

type decoratedCluster struct{ *goredis.ClusterClient }

func TestDecoratedClusterPreservesCompleteScan(t *testing.T) {
	alpha := newProtocolFixture(t)
	beta := newProtocolFixture(t)
	if err := beta.store(32).Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "beta-document", Text: "near"}})); err != nil {
		t.Fatal(err)
	}
	cluster := goredis.NewClusterClient(&goredis.ClusterOptions{
		ClusterSlots: func(context.Context) ([]goredis.ClusterSlot, error) {
			return []goredis.ClusterSlot{
				{Start: 0, End: 8191, Nodes: []goredis.ClusterNode{{Addr: "127.0.0.1:17001"}}},
				{Start: 8192, End: 16383, Nodes: []goredis.ClusterNode{{Addr: "127.0.0.1:17002"}}},
			}, nil
		},
		NewClient: func(opts *goredis.Options) *goredis.Client {
			client := goredis.NewClient(opts)
			if opts.Addr == "127.0.0.1:17001" {
				client.AddHook(alpha)
			} else {
				client.AddHook(beta)
			}
			return client
		},
	})
	cluster.AddHook(alpha)
	t.Cleanup(func() { _ = cluster.Close() })
	for _, tc := range []struct {
		name   string
		client RedisClient
	}{
		{"native", cluster},
		{"decorated", &decoratedCluster{cluster}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := NewStore(t.Context(), StoreConfig{Client: tc.client, ScanNodes: cluster.ForEachMaster, IndexName: "documents", Space: fixtureSpace})
			if err != nil {
				t.Fatal(err)
			}
			budget := fixtureBudget()
			meter, err := budget.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer meter.Close()
			grant := scanGrant{meter: meter, grant: budget}
			selected, err := grant.selectRecords(t.Context(), store, filter.IsNull("absent"))
			if err != nil {
				t.Fatal(err)
			}
			if len(selected) != 1 {
				t.Fatalf("complete scan returned %d records, want beta's record; alpha scans=%d beta scans=%d", len(selected), alpha.scans, beta.scans)
			}
		})
	}
}
