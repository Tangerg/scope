package s3vectors

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3vectors"

	"github.com/Tangerg/scope/core/vectorstore/filter"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestUnfilteredSearchDoesNotRequireListPermission(t *testing.T) {
	fixture := newProtocolFixture(t, &document.Document{ID: "one", Text: "text"})
	fixture.status["/ListVectors"] = http.StatusForbidden
	store := fixture.store()
	response, err := store.Query(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{TopK: 1}})
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != "one" {
		t.Fatalf("native query = %v, %v", response, err)
	}
	if slices.Contains(fixture.paths, "/ListVectors") {
		t.Fatal("unfiltered operation required vector listing")
	}
}

func TestBindingAndScanBudgetFailWithoutPartialResults(t *testing.T) {
	f := newProtocolFixture(t, &document.Document{ID: "one", Text: "hit"}, &document.Document{ID: "two", Text: "hit"})
	s := f.store()
	before := len(f.paths)
	q := &vectorstore.Query{Space: "other", Values: []float64{1, 0}}
	if _, err := s.Query(t.Context(), q); !errors.Is(err, vectorstore.ErrIncompatibleSpace) || len(f.paths) != before {
		t.Fatal(err, f.paths)
	}
	q.Space = fixtureSpace
	q.Options.Filter = filter.IsNull("absent")
	if _, err := s.Query(t.Context(), q); !errors.Is(err, vectorstore.ErrUnsupportedFilter) || len(f.paths) != before {
		t.Fatal(err, f.paths)
	}
	for _, budget := range []vectorstore.ScanBudget{{MaxRecords: 1, MaxBytes: 1 << 20, Timeout: time.Second}, {MaxRecords: 2, MaxBytes: 1, Timeout: time.Second}} {
		if docs, err := s.Scan(t.Context(), vectorstore.ScanRequest{Filter: nil, Budget: budget}); docs != nil || !errors.Is(err, vectorstore.ErrScanBudget) {
			t.Fatalf("partial scan %v, %v", docs, err)
		}
	}
	f.index["metadataConfiguration"] = map[string]any{"nonFilterableMetadataKeys": NonFilterableKeys("recreated")}
	q.Options.Filter = nil
	if _, err := s.Query(t.Context(), q); !errors.Is(err, ErrIncompatibleIndex) {
		t.Fatal("used replaced index", err)
	}
}

type loseSecondAck struct {
	VectorClient
	calls int
}

func (l *loseSecondAck) PutVectors(ctx context.Context, in *awss3.PutVectorsInput, opts ...func(*awss3.Options)) (*awss3.PutVectorsOutput, error) {
	l.calls++
	out, err := l.VectorClient.PutVectors(ctx, in, opts...)
	if l.calls == 2 && err == nil {
		return nil, errors.New("lost acknowledgment after write")
	}
	return out, err
}

func TestLostWriteAckKeepsEarlierEvidenceAndStops(t *testing.T) {
	f := newProtocolFixture(t)
	cfg := f.config()
	client := &loseSecondAck{VectorClient: cfg.Client}
	cfg.Client = client
	s, err := NewStore(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	docs := make([]*document.Document, 1001)
	for i := range docs {
		docs[i] = &document.Document{ID: strconv.Itoa(i), Text: "record"}
	}
	err = s.Upsert(t.Context(), fixtureBatch(docs))
	w, ok := errors.AsType[*vectorstore.WriteError](err)
	if !ok || len(w.Outcome().Acknowledged) != 500 || len(w.Outcome().Unconfirmed) != 500 || len(w.Outcome().Unattempted) != 1 || w.Outcome().Acknowledged[499] != "499" || w.Outcome().Unconfirmed[0] != "500" || w.Outcome().Unattempted[0] != "1000" || client.calls != 2 {
		t.Fatalf("evidence %v, calls=%d", err, client.calls)
	}
}
