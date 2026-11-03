package chat_test

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/document"
	corerank "github.com/Tangerg/scope/core/rerank"
	"github.com/Tangerg/scope/rag"
	ragchat "github.com/Tangerg/scope/rag/chat"
	ragrank "github.com/Tangerg/scope/rag/rerank"
)

func TestSelectedPromptMarkersResolveBackToExactSourceBytes(t *testing.T) {
	type original struct{ source, revision string }
	originals := map[original][]byte{}
	saved := func(source, excerpt string) *document.Document {
		raw := []byte("prefix:" + excerpt + ":suffix")
		p := document.NewProvenance(source, raw)
		p.Bytes = &document.ByteRange{Start: 7, End: int64(7 + len(excerpt))}
		originals[original{source, p.Revision}] = raw
		// Reused IDs do not identify the same passage across corpora or revisions.
		return &document.Document{ID: "row-1", Text: excerpt, Provenance: p}
	}
	old := saved("manual", "before the edit")
	current := saved("manual", "after the edit")
	other := saved("guide", "another source")
	large := saved("long", strings.Repeat("excluded ", 96))
	local := &document.Document{ID: "scratch", Text: "temporary note", Provenance: (*document.Provenance)(nil).Split("scratch", 0, 1)}
	source := func(candidates rag.Candidates) rag.Retriever {
		return rag.RetrieverFunc(func(_ context.Context, q rag.Query) (rag.Candidates, error) {
			if q.Text() != "index terms" {
				return nil, fmt.Errorf("source received %q", q.Text())
			}
			return candidates.Clone(), nil
		})
	}
	first, buildErr := rag.WithRefiners(source(rag.Candidates{candidate(old, .2), candidate(current, .6), candidate(old, .9), candidate(other, .3), candidate(large, .8), candidate(local, .7)}), rag.Dedup())
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	fused, fusionErr := rag.ReciprocalRankFusion(rag.FusionRetrieverConfig{}, first, source(rag.Candidates{candidate(current), candidate(other)}))
	if fusionErr != nil {
		t.Fatal(fusionErr)
	}
	// A formatter is allowed to manipulate its own view, not the source record.
	formatter := document.FormatterFunc(func(d *document.Document) (string, error) {
		text := d.Text
		d.ID = "rewritten-id"
		d.Provenance = nil
		return text, nil
	})
	wanted := []string{large.Text, current.Text, other.Text, old.Text, local.Text}
	ranker, rankErr := ragrank.NewRefiner(ragrank.RefinerConfig{Formatter: formatter, Model: corerank.ModelFunc(func(_ context.Context, req *corerank.Request) (*corerank.Response, error) {
		if len(req.Documents) != len(wanted) {
			return nil, fmt.Errorf("lost a revision or retained a duplicate: %v", req.Documents)
		}
		var hits []*corerank.Result
		for i, text := range wanted {
			j := slices.Index(req.Documents, text)
			if j < 0 {
				return nil, fmt.Errorf("missing candidate %q", text)
			}
			hits = append(hits, &corerank.Result{Index: j, Score: corerank.Score(1 - float64(i)/10)})
		}
		return corerank.NewResponse(hits, nil)
	})})
	if rankErr != nil {
		t.Fatal(rankErr)
	}
	ranked, refineErr := rag.WithRefiners(fused, ranker)
	if refineErr != nil {
		t.Fatal(refineErr)
	}
	search, rewriteErr := rag.WithTransformers(ranked, rag.TransformerFunc(func(_ context.Context, q rag.Query) (rag.Query, error) { return rag.NewQuery("index terms") }))
	if rewriteErr != nil {
		t.Fatal(rewriteErr)
	}
	augmenter, augErr := ragchat.NewContextualAugmenter(ragchat.ContextualAugmenterConfig{Formatter: formatter, TokenCounter: contextBytesCounter{}, MaxContextTokens: 320})
	if augErr != nil {
		t.Fatal(augErr)
	}
	prep, prepErr := ragchat.NewPreparer(ragchat.PreparerConfig{Retriever: search, Augmenter: augmenter})
	if prepErr != nil {
		t.Fatal(prepErr)
	}
	input := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("what changed?"))}}
	prepared, prepareErr := prep.Prepare(t.Context(), input)
	if prepareErr != nil {
		t.Fatal(prepareErr)
	}
	const answer = "The current manual changed [1]. A second supposed source [9]."
	response, callErr := prepared.Call(t.Context(), chat.ModelFunc(func(_ context.Context, r *chat.Request) (*chat.Response, error) {
		prompt := r.Messages[0].Text()
		_, body, ok := strings.Cut(prompt, "\n\n---------------------\n")
		if !ok {
			return nil, errors.New("missing evidence block")
		}
		block, tail, ok := strings.Cut(body, "\n---------------------\n\nQuery: ")
		if !ok || !strings.HasPrefix(tail, "what changed?\n") {
			return nil, fmt.Errorf("prompt lost original question: %q", tail)
		}
		var items []struct{ Citation, ID, Content string }
		if e := jsonv2.Unmarshal([]byte(block), &items, jsonv2.MatchCaseInsensitiveNames(true)); e != nil {
			return nil, e
		}
		if len(items) != 4 || strings.Contains(block, "excluded ") || strings.Contains(block, "rewritten-id") {
			return nil, fmt.Errorf("unexpected evidence: %s", block)
		}
		for i, row := range items {
			if row.Citation != rag.CitationMarker(i) || row.Content != wanted[i+1] {
				return nil, fmt.Errorf("prompt row=%+v", row)
			}
		}
		r.Messages[0] = chat.NewUserMessage(chat.NewTextPart("changed by the model"))
		msg := chat.NewAssistantMessage(chat.NewTextPart(answer))
		return chat.NewResponse(&chat.Output{Message: &msg, FinishReason: chat.FinishReasonStop}, nil)
	}))
	if callErr != nil || response.Text() != answer {
		t.Fatalf("response=%v err=%v", response, callErr)
	}
	// The next collaborator receives the same frozen prompt even if the first
	// model tried to mutate its request.
	_, retryErr := prepared.Call(t.Context(), chat.ModelFunc(func(_ context.Context, r *chat.Request) (*chat.Response, error) {
		if !strings.Contains(r.Messages[0].Text(), "what changed?") {
			return nil, errors.New("previous model rewrote the prompt")
		}
		msg := chat.NewAssistantMessage(chat.NewTextPart("same evidence"))
		return chat.NewResponse(&chat.Output{Message: &msg, FinishReason: chat.FinishReasonStop}, nil)
	}))
	if retryErr != nil {
		t.Fatal(retryErr)
	}
	evidence := prepared.Evidence()
	if evidence.Citations.Len() != 4 || len(evidence.Candidates) != 5 || evidence.Candidates[0].Document.Text != large.Text {
		t.Fatalf("ranked=%d admitted=%d", len(evidence.Candidates), evidence.Citations.Len())
	}
	for i, expected := range []*document.Document{current, other, old} {
		d, e := evidence.Citations.Resolve(rag.CitationMarker(i))
		if e != nil {
			t.Fatal(e)
		}
		p := d.Provenance
		raw, ok := originals[original{p.Source, p.Revision}]
		if !ok || p.Bytes == nil || d.ID != expected.ID || p.Source != expected.Provenance.Source || p.Revision != expected.Provenance.Revision || string(raw[p.Bytes.Start:p.Bytes.End]) != d.Text || d.Text != expected.Text {
			t.Fatalf("incorrect revision for marker %d: %+v", i+1, d)
		}
		d.Provenance.Revision = "caller-edited"
		d.Text = "changed"
		again, e := evidence.Citations.Resolve(rag.CitationMarker(i))
		if e != nil || again.Provenance.Revision != expected.Provenance.Revision || again.Text != expected.Text {
			t.Fatal("caller rewrote a frozen citation", e)
		}
	}
	if d, e := evidence.Citations.Resolve("[4]"); d != nil || !errors.Is(e, document.ErrUnversionedSource) {
		t.Fatal("temporary note became a verifiable reference", d, e)
	}
	for _, marker := range []string{"[9]", "[05]", "[+1]", " [1]", "[0]", "[9999999999999999999999999]", "[]"} {
		if got, e := evidence.Citations.Resolve(marker); got != nil || !errors.Is(e, rag.ErrUnknownCitation) {
			t.Fatalf("invented marker=%q doc=%v err=%v", marker, got, e)
		}
	}
	if evidence.Citations.Len() != 4 || input.Messages[0].Text() != "what changed?" {
		t.Fatal("model output changed source selection or input")
	}
}
