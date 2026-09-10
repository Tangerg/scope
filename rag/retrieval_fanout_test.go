package rag_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/Tangerg/scope/rag"
)

func TestExpansionFusesIndependentRankings(t *testing.T) {
	shared := identifiedDocument(t, "shared", "shared evidence")
	first := identifiedDocument(t, "first", "first-only evidence")
	second := identifiedDocument(t, "second", "second-only evidence")
	expanded, err := rag.WithExpander(rag.ExpansionConfig{
		Expander: rag.ExpanderFunc(func(context.Context, rag.Query) ([]rag.Query, error) {
			return []rag.Query{mustQuery(t, "first"), mustQuery(t, "second")}, nil
		}),
		Retriever: rag.RetrieverFunc(func(_ context.Context, query rag.Query) (rag.Candidates, error) {
			if query.Text() == "first" {
				return rag.Candidates{candidate(shared, 0.01), candidate(first, 1_000)}.Clone(), nil
			}
			return rag.Candidates{candidate(shared, -100), candidate(second, 10_000)}.Clone(), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := expanded.Retrieve(t.Context(), mustQuery(t, "source"))
	if err != nil {
		t.Fatal(err)
	}
	wantScore := 2.0 / float64(rag.DefaultReciprocalRankConstant+1)
	if len(got) != 3 || got[0].Document.ID != "shared" || math.Abs(got[0].Score.Float64()-wantScore) > 1e-12 ||
		got[1].Document.ID != "first" || got[2].Document.ID != "second" {
		t.Fatalf("expanded ranking = %#v", got)
	}
}

func TestRetrievalFanoutHonorsConfiguredLimit(t *testing.T) {
	for _, expansion := range []bool{false, true} {
		for _, limit := range []int{0, 1, 2} {
			t.Run(fmt.Sprintf("expansion=%t/limit=%d", expansion, limit), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					const count = 12
					var calls, active, peak atomic.Int64
					release := make(chan struct{})
					base := rag.RetrieverFunc(func(context.Context, rag.Query) (rag.Candidates, error) {
						calls.Add(1)
						current := active.Add(1)
						defer active.Add(-1)
						for previous := peak.Load(); current > previous; previous = peak.Load() {
							if peak.CompareAndSwap(previous, current) {
								break
							}
						}
						<-release
						return nil, nil
					})
					config := rag.ReciprocalRankFusionConfig{MaxConcurrentRetrievals: limit}
					var retriever rag.Retriever
					var err error
					if expansion {
						retriever, err = rag.WithExpander(rag.ExpansionConfig{
							Retriever: base, Fusion: config,
							Expander: rag.ExpanderFunc(func(context.Context, rag.Query) ([]rag.Query, error) {
								queries := make([]rag.Query, count)
								for index := range queries {
									queries[index] = mustQuery(t, fmt.Sprintf("query %d", index))
								}
								return queries, nil
							}),
						})
					} else {
						children := make([]rag.Retriever, count)
						for index := range children {
							children[index] = base
						}
						retriever, err = rag.ReciprocalRankFusion(config, children...)
					}
					if err != nil {
						t.Fatal(err)
					}
					completed := make(chan error, 1)
					go func() {
						_, retrieveErr := retriever.Retrieve(t.Context(), mustQuery(t, "source"))
						completed <- retrieveErr
					}()
					synctest.Wait()
					bound := limit
					if bound == 0 {
						bound = rag.DefaultMaxConcurrentRetrievals
					}
					if got := calls.Load(); got != int64(bound) {
						t.Errorf("blocked active calls = %d, want %d", got, bound)
					}
					close(release)
					if retrieveErr := <-completed; retrieveErr != nil {
						t.Fatal(retrieveErr)
					}
					if calls.Load() != count || peak.Load() > int64(bound) {
						t.Fatalf("calls = %d, peak = %d, bound = %d", calls.Load(), peak.Load(), bound)
					}
				})
			})
		}
	}
}

func TestRetrievalFanoutRejectsNegativeLimit(t *testing.T) {
	config := rag.ReciprocalRankFusionConfig{MaxConcurrentRetrievals: -1}
	base := &fakeRetriever{}
	if _, err := rag.ReciprocalRankFusion(config, base); !errors.Is(err, rag.ErrInvalidRetrievalConcurrency) {
		t.Fatalf("fusion configuration error = %v", err)
	}
	if _, err := rag.WithExpander(rag.ExpansionConfig{
		Retriever: base, Fusion: config,
		Expander: rag.ExpanderFunc(func(_ context.Context, query rag.Query) ([]rag.Query, error) {
			return []rag.Query{query}, nil
		}),
	}); !errors.Is(err, rag.ErrInvalidRetrievalConcurrency) {
		t.Fatalf("expansion configuration error = %v", err)
	}
}
