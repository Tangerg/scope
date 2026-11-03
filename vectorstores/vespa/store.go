package vespa

import (
	"bytes"
	"cmp"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/Tangerg/scope/core/httpclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

const (
	queryGroupSize = 128
	visitPageSize  = 128
)

var _ vectorstore.Upserter = (*Store)(nil)

type Store struct {
	endpoint         string
	schemaName       string
	namespace        string
	fields           schemaFields
	queryTensorName  string
	rankingProfile   string
	space            vectorstore.SpaceID
	dimensions       int
	httpClient       *http.Client
	maxResponseBytes int64
}

// NewStore performs no I/O. The application package owns its schema and ranking
// profile; Vespa exposes no standard schema API on the Document API endpoint.
func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	ownedHTTP, err := httpclient.New(ctx, httpclient.Config{RejectRedirects: true, HTTPClient: config.HTTPClient, BaseURL: config.Endpoint})
	if err != nil {
		return nil, err
	}
	return &Store{endpoint: strings.TrimRight(config.Endpoint, "/"), schemaName: config.SchemaName, namespace: config.DocumentNamespace(),
		fields: schemaFields{content: config.ContentField, embedding: config.EmbeddingField}, queryTensorName: config.QueryTensorName,
		rankingProfile: config.RankingProfile, space: config.Space, dimensions: config.Dimensions,
		httpClient: ownedHTTP, maxResponseBytes: cmp.Or(config.MaxResponseBytes, DefaultMaxResponseBytes)}, nil
}

func (s *Store) Upsert(ctx context.Context, batch *vectorstore.Batch) error {
	if err := batch.ValidateFor(s.space); err != nil {
		return err
	}
	records := make([]nativeRecord, 0, len(batch.Records))
	ids := make([]string, 0, len(batch.Records))
	for _, r := range batch.Records {
		doc := r.Document
		if doc.Media != nil {
			return vectorstore.ErrInvalidDocument
		}
		if err := validateText(doc.ID); err != nil {
			return err
		}
		if err := validateText(doc.Text); err != nil {
			return err
		}
		facts, err := doc.MarshalStoredMetadata()
		if err != nil {
			return err
		}
		values, err := floatVector(r.Values, s.dimensions)
		if err != nil {
			return err
		}
		records = append(records, nativeRecord{document: doc, metadata: string(facts), vector: values})
		ids = append(ids, doc.ID)
	}
	for i, record := range records {
		if err := ctx.Err(); err != nil {
			return &vectorstore.WriteError{Err: err, Acknowledged: ids[:i], Unattempted: ids[i:]}
		}
		fields := map[string]any{s.fields.content: record.document.Text, metadataField: record.metadata, s.fields.embedding: struct {
			Values []float32 `json:"values"`
		}{record.vector}}
		raw, status, err := s.sendJSON(ctx, http.MethodPost, s.documentPath(record.document.ID), struct {
			Fields map[string]any `json:"fields"`
		}{fields})
		if err == nil {
			err = s.mutationAcknowledgment(raw, status, record.document.ID)
		}
		if err != nil {
			return &vectorstore.WriteError{Err: err, Acknowledged: ids[:i], Unconfirmed: ids[i : i+1], Unattempted: ids[i+1:]}
		}

	}
	return nil
}

func (s *Store) Scan(ctx context.Context, request *vectorstore.Query, budget vectorstore.ScanBudget) (*vectorstore.SearchResponse, error) {
	if err := request.ValidateFor(s.space); err != nil {
		return nil, err
	}
	if err := request.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, err
	}
	narrowed, err := floatVector(request.Values, s.dimensions)
	if err != nil {
		return nil, err
	}
	meter, err := budget.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer meter.Close()
	records, width, err := s.readSource(meter)
	if err != nil {
		return nil, err
	}
	selected, err := selectRecords(records, request.Options.Filter)
	if err != nil {
		return nil, err
	}
	if len(selected) == 0 {
		return &vectorstore.SearchResponse{Results: []*vectorstore.SearchResult{}}, nil
	}
	results := make([]*vectorstore.SearchResult, 0, len(selected))
	for start := 0; start < len(selected); start += queryGroupSize {
		group := selected[start:min(start+queryGroupSize, len(selected))]
		ids := make([]string, len(group))
		expected := make(map[string]bool, len(group))
		for index, record := range group {
			literal, err := quoteLiteral(s.nativeID(record.document.ID))
			if err != nil {
				return nil, err
			}
			ids[index] = literal
			expected[record.document.ID] = false
		}
		yql := fmt.Sprintf("select %s, %s, %s, %s from %s where {approximate:false,targetHits:%d}nearestNeighbor(%s,%s) and %s in (%s)", s.fields.content, s.fields.embedding, metadataField, nativeIDField, s.schemaName, len(group), s.fields.embedding, s.queryTensorName, identityAttribute, strings.Join(ids, ","))
		body := map[string]any{"yql": yql, "hits": len(group), "ranking": s.rankingProfile, "presentation.summary": defaultSummary,
			"input.query(" + s.queryTensorName + ")": struct {
				Values []float32 `json:"values"`
			}{narrowed}}
		hits, err := s.query(meter, body)
		if err != nil {
			return nil, err
		}
		if len(hits) != len(group) {
			return nil, errors.New("vespa: exact query omitted selected documents; check native visibility and query limits")
		}
		for _, hit := range hits {
			rawID, present, err := hit.Fields.Decode[string](nativeIDField)
			if err != nil || !present {
				return nil, errors.New("vespa: search summary lacks its native document ID")
			}
			id, err := s.documentID(rawID)
			if err != nil {
				return nil, err
			}
			seen, belongs := expected[id]
			if !belongs || seen {
				return nil, errors.New("vespa: search returned an unexpected or repeated identity")
			}
			expected[id] = true
			record, err := decodeRecord(id, hit.Fields, s.fields, true)
			if err != nil {
				return nil, err
			}
			if len(record.vector) != width {
				return nil, errors.New("vespa: search tensor dimensions changed")
			}
			if hit.Relevance == nil {
				return nil, errors.New("vespa: search hit lacks relevance")
			}
			score := vectorstore.Score(*hit.Relevance)
			if err = score.Validate(); err != nil {
				return nil, err
			}
			matches, err := matchesMetadata(request.Options.Filter, record.document.Metadata)
			if err != nil {
				return nil, err
			}
			if matches && score >= request.Options.MinScore {
				results = append(results, &vectorstore.SearchResult{Document: record.document, Score: score})
			}
		}
	}
	slices.SortStableFunc(results, func(left, right *vectorstore.SearchResult) int { return cmp.Compare(right.Score, left.Score) })
	results = results[:min(len(results), request.Options.ResultLimit())]
	response := &vectorstore.SearchResponse{Results: results}
	if err := request.ValidateResponse(response); err != nil {
		return nil, err
	}
	return response, nil
}

func (s *Store) DeleteMatching(ctx context.Context, predicate filter.Predicate, budget vectorstore.ScanBudget) error {
	if predicate == nil {
		return vectorstore.ErrMissingFilter
	}
	if err := predicate.Validate(); err != nil {
		return err
	}
	meter, err := budget.Begin(ctx)
	if err != nil {
		return err
	}
	defer meter.Close()
	ctx = meter.Context()
	records, _, err := s.readSource(meter)
	if err != nil {
		return err
	}
	selected, err := selectRecords(records, predicate)
	if err != nil {
		return err
	}
	for _, record := range selected {
		literal, err := quoteLiteral(record.metadata)
		if err != nil {
			return err
		}
		parameters := url.Values{"condition": {s.schemaName + "." + metadataField + " == " + literal}}
		raw, status, err := s.sendJSON(ctx, http.MethodDelete, s.documentPath(record.document.ID)+"?"+parameters.Encode(), nil)
		if err != nil {
			return err
		}
		if status == http.StatusPreconditionFailed {
			continue
		}
		if err := s.mutationAcknowledgment(raw, status, record.document.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) readSource(meter *vectorstore.ScanMeter) ([]nativeRecord, int, error) {
	records := make([]nativeRecord, 0)
	seenIDs := make(map[string]bool)
	seenTokens := make(map[string]bool)
	width := s.dimensions
	parameters := url.Values{"fieldSet": {"[document]"}, "wantedDocumentCount": {strconv.Itoa(visitPageSize)}}
	for {
		raw, status, err := s.wire(meter.Context(), http.MethodGet, s.documentPath("")+"?"+parameters.Encode(), nil, meter)
		if err != nil {
			return nil, 0, err
		}
		if status != http.StatusOK {
			return nil, 0, fmt.Errorf("vespa: visit status %d", status)
		}
		var response visitResponse
		if err := jsonv2.Unmarshal(raw, &response); err != nil {
			return nil, 0, err
		}
		if response.DocumentCount == nil || *response.DocumentCount != len(response.Documents) {
			return nil, 0, errors.New("vespa: visit count and documents disagree")
		}
		if err := meter.ChargePage(len(response.Documents), 0); err != nil {
			return nil, 0, err
		}
		for _, item := range response.Documents {
			id, err := s.documentID(item.ID)
			if err != nil {
				return nil, 0, err
			}
			if seenIDs[id] {
				return nil, 0, errors.New("vespa: visit repeated a document identity")
			}
			seenIDs[id] = true
			record, err := decodeRecord(id, item.Fields, s.fields, false)
			if err != nil {
				return nil, 0, err
			}
			if width != 0 && width != len(record.vector) {
				return nil, 0, errors.New("vespa: native tensor dimensions disagree")
			}
			width = len(record.vector)
			records = append(records, record)
		}
		if response.Continuation == "" {
			return records, width, nil
		}
		if seenTokens[response.Continuation] {
			return nil, 0, errors.New("vespa: visit continuation did not advance")
		}
		seenTokens[response.Continuation] = true
		parameters.Set("continuation", response.Continuation)
	}
}

func (s *Store) query(meter *vectorstore.ScanMeter, body map[string]any) ([]queryHit, error) {
	raw, status, err := s.wire(meter.Context(), http.MethodPost, "/search/", body, meter)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("vespa: search status %d", status)
	}
	var response queryResponse
	if err := jsonv2.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	if len(response.Root.Errors) != 0 {
		return nil, fmt.Errorf("vespa: search errors: %v", response.Root.Errors)
	}
	coverage := response.Root.Coverage
	if coverage == nil {
		return nil, errors.New("vespa: search is missing coverage")
	}
	if !coverage.Full || coverage.Coverage != 100 {
		return nil, fmt.Errorf("vespa: search evaluated %d%% of the corpus (full=%t)", coverage.Coverage, coverage.Full)
	}
	for cause, degraded := range coverage.Degraded {
		if degraded {
			return nil, fmt.Errorf("vespa: search reports degraded coverage: %s", cause)
		}
	}
	return response.Root.Children, nil
}

func (s *Store) sendJSON(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	return s.wire(ctx, method, path, body, nil)
}
func (s *Store) wire(ctx context.Context, method, path string, body any, meter *vectorstore.ScanMeter) ([]byte, int, error) {
	maximum := cmp.Or(s.maxResponseBytes, DefaultMaxResponseBytes)
	if meter != nil {
		maximum = min(maximum, meter.RemainingBytes())
		if maximum == 0 {
			return nil, 0, vectorstore.ErrScanBudget
		}
	}
	var reader io.Reader
	if body != nil {
		encoded, err := jsonv2.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, s.endpoint+path, reader)
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := s.httpClient.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil {
		return nil, 0, err
	}
	if meter != nil {
		if err := meter.ChargePage(0, int64(len(raw))); err != nil {
			return nil, 0, err
		}
	}
	if int64(len(raw)) > maximum {
		return nil, 0, fmt.Errorf("vespa: response exceeds %d-byte limit", maximum)
	}
	if response.StatusCode != http.StatusOK && (method != http.MethodDelete || response.StatusCode != http.StatusPreconditionFailed) {
		return nil, 0, fmt.Errorf("vespa: %s status %d: %s", method, response.StatusCode, raw)
	}
	return raw, response.StatusCode, nil
}

func (s *Store) mutationAcknowledgment(raw []byte, status int, id string) error {
	if status != http.StatusOK {
		return fmt.Errorf("vespa: mutation status %d", status)
	}
	var response struct {
		ID string `json:"id"`
	}
	if err := jsonv2.Unmarshal(raw, &response); err != nil {
		return err
	}
	if response.ID != s.nativeID(id) {
		return errors.New("vespa: mutation did not acknowledge the requested native identity")
	}
	return nil
}

func (s *Store) nativeID(id string) string {
	return "id:" + s.namespace + ":" + s.schemaName + "::" + id
}
func (s *Store) documentPath(id string) string {
	segment := url.PathEscape(id)
	if segment == "." || segment == ".." {
		segment = strings.ReplaceAll(segment, ".", "%2E")
	}
	return "/document/v1/" + url.PathEscape(s.namespace) + "/" + url.PathEscape(s.schemaName) + "/docid/" + segment
}
func (s *Store) documentID(raw string) (string, error) {
	id, present := strings.CutPrefix(raw, s.nativeID(""))
	if !present || id == "" {
		return "", fmt.Errorf("vespa: document %q is outside the configured native identity scope", raw)
	}
	if err := validateText(id); err != nil {
		return "", err
	}
	return id, nil
}

type visitDocument struct {
	ID     string       `json:"id"`
	Fields metadata.Map `json:"fields"`
}
type visitResponse struct {
	DocumentCount *int            `json:"documentCount"`
	Documents     []visitDocument `json:"documents"`
	Continuation  string          `json:"continuation"`
}
type queryHit struct {
	Relevance *float64     `json:"relevance"`
	Fields    metadata.Map `json:"fields"`
}
type queryResponse struct {
	Root struct {
		Errors   []queryError   `json:"errors"`
		Coverage *queryCoverage `json:"coverage"`
		Children []queryHit     `json:"children"`
	} `json:"root"`
}
type queryError struct {
	Code    int    `json:"code"`
	Summary string `json:"summary"`
	Message string `json:"message"`
}
type queryCoverage struct {
	Coverage int             `json:"coverage"`
	Full     bool            `json:"full"`
	Degraded map[string]bool `json:"degraded"`
}

func selectRecords(records []nativeRecord, predicate filter.Predicate) ([]nativeRecord, error) {
	selected := make([]nativeRecord, 0, len(records))
	for _, record := range records {
		matches, err := matchesMetadata(predicate, record.document.Metadata)
		if err != nil {
			return nil, err
		}
		if matches {
			selected = append(selected, record)
		}
	}
	return selected, nil
}

func matchesMetadata(predicate filter.Predicate, facts metadata.Map) (bool, error) {
	if predicate == nil {
		return true, nil
	}
	values, err := facts.Values()
	if err != nil {
		return false, err
	}
	return filter.Match(predicate, values)
}
