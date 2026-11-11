package elastic

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"net/http"
	"net/url"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

const (
	scrollKeepAlive = "1m"
	scrollPath      = "/_search/scroll"
)

type ScrollConfig struct {
	Client  *Client
	Index   string
	Request []byte
	Budget  vectorstore.ScanBudget
	Decode  func(Hit) (*document.Document, error)
}

// Scroll owns exhaustive, budgeted membership and cursor cleanup. Decode belongs
// to the native schema owner; this protocol cannot infer vector representations.
type Scroll struct {
	client *Client
	index  string
	body   []byte
	budget vectorstore.ScanBudget
	decode func(Hit) (*document.Document, error)
}

func NewScroll(config ScrollConfig) (*Scroll, error) {
	if err := config.Client.validate(); err != nil {
		return nil, err
	}
	if config.Decode == nil {
		return nil, errors.New("elastic: scroll requires a native record decoder")
	}
	if err := indexName(config.Index).validate(); err != nil {
		return nil, err
	}
	if err := config.Budget.Validate(); err != nil {
		return nil, err
	}
	if !jsontext.Value(config.Request).IsValid() {
		return nil, errors.New("elastic: scroll requires a valid search request")
	}
	return &Scroll{client: config.Client, index: config.Index, body: bytes.Clone(config.Request), budget: config.Budget, decode: config.Decode}, nil
}

func (s *Scroll) Select(ctx context.Context, predicate filter.Predicate) (selected map[string]Version, err error) {
	if s == nil || s.client == nil || s.decode == nil {
		return nil, errors.New("elastic: uninitialized scroll")
	}
	if !lo.IsNil(predicate) {
		if err = predicate.Validate(); err != nil {
			return nil, err
		}
	}
	meter, err := s.budget.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer meter.Close()
	ctx = meter.Context()
	raw, callErr := s.client.Read(ctx, http.MethodPost, "/"+s.index+"/_search", url.Values{"scroll": {scrollKeepAlive}, "allow_partial_search_results": {"false"}}, s.body, meter.RemainingBytes())
	cursor := scrollCursor{client: s.client}
	defer func() {
		err = errors.Join(err, cursor.close(ctx))
		if err != nil {
			selected = nil
		}
	}()
	selection := scrollSelection{scroll: s, predicate: predicate, meter: meter, seen: make(map[string]struct{}), selected: make(map[string]Version)}
	for {
		page, decodeErr := cursor.decodePage(raw)
		if err = errors.Join(callErr, decodeErr); err != nil {
			return nil, err
		}
		done, foldErr := selection.fold(raw, page)
		if foldErr != nil {
			return nil, foldErr
		}
		if done {
			return selection.selected, nil
		}
		payload, encodeErr := jsonv2.Marshal(struct {
			ID       string `json:"scroll_id"`
			Lifetime string `json:"scroll"`
		}{ID: page.ScrollID, Lifetime: scrollKeepAlive})
		if encodeErr != nil {
			return nil, encodeErr
		}
		remaining := meter.RemainingBytes()
		if remaining <= 0 {
			return nil, vectorstore.ErrScanBudget
		}
		raw, callErr = s.client.Read(ctx, http.MethodPost, scrollPath, nil, payload, remaining)
	}
}

// scrollSelection folds scroll pages into the selected membership. The
// declared total is charged once, before any hit is decoded, and every later
// page must repeat it.
type scrollSelection struct {
	scroll    *Scroll
	predicate filter.Predicate
	meter     *vectorstore.ScanMeter
	total     *int64
	seen      map[string]struct{}
	selected  map[string]Version
}

// fold charges and admits one page; done reports the empty page that ends a
// scroll which returned every declared document.
func (s *scrollSelection) fold(raw []byte, page SearchResponse) (bool, error) {
	if err := s.meter.ChargePage(0, int64(len(raw))); err != nil {
		return false, err
	}
	if err := page.Validate(s.scroll.index); err != nil {
		return false, err
	}
	if s.total == nil {
		s.total = page.Hits.Total.Value
		if err := s.meter.ChargePage(int(*s.total), 0); err != nil {
			return false, err
		}
	}
	if *s.total != *page.Hits.Total.Value || page.ScrollID == "" {
		return false, errors.New("elastic: native scroll lost its total or cursor")
	}
	hits := *page.Hits.Hits
	if len(hits) == 0 {
		if int64(len(s.seen)) != *s.total {
			return false, errors.New("elastic: native scroll ended before every document")
		}
		return true, nil
	}
	for _, hit := range hits {
		if err := s.admit(hit); err != nil {
			return false, err
		}
	}
	if int64(len(s.seen)) > *s.total {
		return false, errors.New("elastic: native scroll exceeded its declared total")
	}
	return false, nil
}

func (s *scrollSelection) admit(hit Hit) error {
	if _, duplicate := s.seen[hit.ID]; duplicate {
		return errors.New("elastic: native scroll repeated a document ID")
	}
	s.seen[hit.ID] = struct{}{}
	version, matched, err := s.scroll.member(hit, s.predicate)
	if err != nil {
		return err
	}
	if matched {
		s.selected[hit.ID] = version
	}
	return nil
}

// member decodes one scrolled record and reports whether it satisfies the
// predicate, with the version a conditional delete must hold it at.
func (s *Scroll) member(hit Hit, predicate filter.Predicate) (Version, bool, error) {
	if err := hit.Validate(); err != nil {
		return Version{}, false, err
	}
	doc, err := s.decode(hit)
	if err != nil {
		return Version{}, false, err
	}
	if err = vectorstore.ValidateDocument(doc); err != nil {
		return Version{}, false, err
	}
	if doc.ID != hit.ID {
		return Version{}, false, errors.New("elastic: decoded document changed native identity")
	}
	if !lo.IsNil(predicate) {
		matched, matchErr := filter.Match(predicate, doc.Metadata)
		if matchErr != nil || !matched {
			return Version{}, false, matchErr
		}
	}
	version, err := hit.Version()
	return version, err == nil, err
}
