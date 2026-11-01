package etl_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/etl"
	"github.com/Tangerg/scope/etl/text"
)

func TestOversizedStreamCannotAttestItsSuffixOnRetry(t *testing.T) {
	b, err := etl.NewSourceBudget(4)
	if err != nil {
		t.Fatal(err)
	}
	r, err := text.NewReader(strings.NewReader("ABCDEtail"), text.ReaderConfig{SourceName: "report.txt", SourceBudget: b})
	if err != nil {
		t.Fatal(err)
	}
	if docs, firstErr := r.Read(t.Context()); !errors.Is(firstErr, etl.ErrSourceTooLarge) || docs != nil {
		t.Fatalf("oversized input returned %v %v", docs, firstErr)
	}
	if docs, again := r.Read(t.Context()); !errors.Is(again, etl.ErrSourceConsumed) || docs != nil {
		t.Fatalf("retry attested a partial report: %+v %v", docs, again)
	}
}

func TestCanceledReadDoesNotSpendTheStreamAndConcurrentReadCannotSplitIt(t *testing.T) {
	p, w := io.Pipe()
	t.Cleanup(func() { _ = p.Close(); _ = w.Close() })
	r, err := text.NewReader(p, text.ReaderConfig{SourceName: "report.txt"})
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = r.Read(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	type answer struct {
		docs []*document.Document
		err  error
	}
	out := make(chan answer, 1)
	go func() { docs, readErr := r.Read(t.Context()); out <- answer{docs, readErr} }()
	// Write completes only after the admitted reader has consumed these bytes.
	if _, err = io.WriteString(w, "first"); err != nil {
		t.Fatal(err)
	}
	if docs, concurrent := r.Read(t.Context()); docs != nil || !errors.Is(concurrent, etl.ErrSourceConsumed) {
		t.Fatalf("concurrent reader entered: %v %v", docs, concurrent)
	}
	if _, err = io.WriteString(w, " last"); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	got := <-out
	if got.err != nil || len(got.docs) != 1 || got.docs[0].Text != "first last" || got.docs[0].Provenance.Revision != revision("first last") {
		t.Fatalf("whole result = %v %v", got.docs, got.err)
	}
}
