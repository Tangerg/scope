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
	selected = make(map[string]Version)
	seen := make(map[string]struct{})
	var total *int64
	for {
		page, decodeErr := cursor.decodePage(raw)
		if err = errors.Join(callErr, decodeErr); err != nil {
			return nil, err
		}
		if err = meter.ChargePage(0, int64(len(raw))); err != nil {
			return nil, err
		}
		if err = page.Validate(s.index); err != nil {
			return nil, err
		}
		// The declared total is charged once, before any hit is decoded.
		if total == nil {
			total = page.Hits.Total.Value
			if err = meter.ChargePage(int(*total), 0); err != nil {
				return nil, err
			}
		}
		if *total != *page.Hits.Total.Value || page.ScrollID == "" {
			return nil, errors.New("elastic: native scroll lost its total or cursor")
		}
		hits := *page.Hits.Hits
		if len(hits) == 0 {
			if int64(len(seen)) != *total {
				return nil, errors.New("elastic: native scroll ended before every document")
			}
			return selected, nil
		}
		for _, hit := range hits {
			if _, duplicate := seen[hit.ID]; duplicate {
				return nil, errors.New("elastic: native scroll repeated a document ID")
			}
			seen[hit.ID] = struct{}{}
			version, matched, memberErr := s.member(hit, predicate)
			if memberErr != nil {
				return nil, memberErr
			}
			if matched {
				selected[hit.ID] = version
			}
		}
		if int64(len(seen)) > *total {
			return nil, errors.New("elastic: native scroll exceeded its declared total")
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
