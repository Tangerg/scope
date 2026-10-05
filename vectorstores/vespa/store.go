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
	"strings"

	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

const queryGroupSize = 128

var (
	_ vectorstore.Indexer       = (*Store)(nil)
	_ vectorstore.Searcher      = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
)

// Store uses native document identity and complete Core metadata.
type Store struct {
	endpoint         string
	schemaName       string
	namespace        string
	fields           schemaFields
	queryTensorName  string
	rankingProfile   string
	embeddingClient  embeddingclient.Client
	documentBatcher  vectorstore.Batcher
	httpClient       *http.Client
	maxResponseBytes int64
}

// NewStore performs no I/O. The application package owns its schema and ranking
// profile; Vespa exposes no standard schema API on the Document API endpoint.
func NewStore(_ context.Context, config StoreConfig) (*Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	client, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, err
	}
	return &Store{endpoint: strings.TrimRight(config.Endpoint, "/"), schemaName: config.SchemaName, namespace: config.Namespace,
		fields: schemaFields{content: config.ContentField, embedding: config.EmbeddingField}, queryTensorName: config.QueryTensorName,
		rankingProfile: config.RankingProfile, embeddingClient: client, documentBatcher: config.DocumentBatcher,
		httpClient: config.HTTPClient, maxResponseBytes: cmp.Or(config.MaxResponseBytes, DefaultMaxResponseBytes)}, nil
}

func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	records := make([]nativeRecord, len(request.Documents))
	for index, doc := range request.Documents {
		if doc.Media != nil {
			return fmt.Errorf("%w: media is unsupported", vectorstore.ErrInvalidDocument)
		}
		if err := validateText(doc.ID); err != nil {
			return err
		}
		if err := validateText(doc.Text); err != nil {
			return err
		}
		facts, err := doc.Metadata.MarshalJSON()
		if err != nil {
			return err
		}
		records[index] = nativeRecord{document: doc, metadata: string(facts)}
	}
	batches, err := request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return err
	}
	_, width, err := s.readSource(ctx)
	if err != nil {
		return err
	}
	position := 0
	for _, batch := range batches {
		texts, err := batch.Texts()
		if err != nil {
			return err
		}
		vectors, err := s.embeddingClient.EmbedTexts(ctx, texts)
		if err != nil {
			return err
		}
		for _, vector := range vectors {
			narrowed, err := floatVector(vector, width)
			if err != nil {
				return err
			}
			if width == 0 {
				width = len(narrowed)
			}
			records[position].vector = narrowed
			position++
		}
	}
	for _, record := range records {
		fields := map[string]any{s.fields.content: record.document.Text, metadataField: record.metadata, s.fields.embedding: struct {
			Values []float32 `json:"values"`
		}{record.vector}}
		raw, status, err := s.sendJSON(ctx, http.MethodPost, s.documentPath(record.document.ID), struct {
			Fields map[string]any `json:"fields"`
		}{fields})
		if err != nil {
			return err
		}
		if err := s.mutationAcknowledgment(raw, status, record.document.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Search(ctx context.Context, request *vectorstore.SearchRequest) (*vectorstore.SearchResponse, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if err := request.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, err
	}
	records, width, err := s.readSource(ctx)
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
	vector, err := s.embeddingClient.EmbedText(ctx, request.Query)
	if err != nil {
		return nil, err
	}
	narrowed, err := floatVector(vector, width)
	if err != nil {
		return nil, err
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
		hits, err := s.query(ctx, body)
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
	if err := response.ValidateFor(request); err != nil {
		return nil, err
	}
	return response, nil
}

func (s *Store) DeleteWhere(ctx context.Context, predicate filter.Predicate) error {
	if predicate == nil {
		return vectorstore.ErrMissingFilter
	}
	if err := predicate.Validate(); err != nil {
		return err
	}
	records, _, err := s.readSource(ctx)
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

func (s *Store) readSource(ctx context.Context) ([]nativeRecord, int, error) {
	records := make([]nativeRecord, 0)
	seenIDs := make(map[string]bool)
	seenTokens := make(map[string]bool)
	width := 0
	parameters := url.Values{"fieldSet": {"[document]"}, "wantedDocumentCount": {"128"}}
	for {
		raw, status, err := s.sendJSON(ctx, http.MethodGet, s.documentPath("")+"?"+parameters.Encode(), nil)
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

func (s *Store) query(ctx context.Context, body map[string]any) ([]queryHit, error) {
	raw, status, err := s.sendJSON(ctx, http.MethodPost, "/search/", body)
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
	maximum := cmp.Or(s.maxResponseBytes, DefaultMaxResponseBytes)
	raw, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil {
		return nil, 0, err
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
	return "/document/v1/" + url.PathEscape(s.namespace) + "/" + url.PathEscape(s.schemaName) + "/docid/" + url.PathEscape(id)
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
