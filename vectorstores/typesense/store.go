package typesense

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/samber/lo"
	nativesense "github.com/typesense/typesense-go/v3/typesense"
	"github.com/typesense/typesense-go/v3/typesense/api"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

const (
	Provider                = "Typesense"
	DefaultCollectionName   = "scope_vector_store"
	DefaultMaxResponseBytes = int64(16 * 1024 * 1024)
	MaxResultsPerPage       = 250
	maxKeysPerDelete        = 100
)

var collectionNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var ErrIncompatibleCollection = errors.New("typesense: collection is incompatible")

type APIClient interface {
	GetCollection(context.Context, string, ...api.RequestEditorFn) (*http.Response, error)
	ExportDocuments(context.Context, string, *api.ExportDocumentsParams, ...api.RequestEditorFn) (*http.Response, error)
	ImportDocumentsWithBody(context.Context, string, *api.ImportDocumentsParams, string, io.Reader, ...api.RequestEditorFn) (*http.Response, error)
	MultiSearchWithBody(context.Context, *api.MultiSearchParams, string, io.Reader, ...api.RequestEditorFn) (*http.Response, error)
	DeleteDocuments(context.Context, string, *api.DeleteDocumentsParams, ...api.RequestEditorFn) (*http.Response, error)
}

type StoreConfig struct {
	Client           APIClient
	CollectionName   string
	EmbeddingModel   embedding.Model
	DocumentBatcher  vectorstore.Batcher
	HybridAlpha      *float32
	MaxResponseBytes int64
}

func (s StoreConfig) Validate() error {
	if lo.IsNil(s.Client) {
		return errors.New("typesense: Client is required")
	}
	if lo.IsNil(s.EmbeddingModel) {
		return errors.New("typesense: EmbeddingModel is required")
	}
	if lo.IsNil(s.DocumentBatcher) {
		return errors.New("typesense: DocumentBatcher is required")
	}
	if !collectionNamePattern.MatchString(cmp.Or(s.CollectionName, DefaultCollectionName)) {
		return errors.New("typesense: CollectionName must be a safe identifier")
	}
	if s.HybridAlpha != nil && (math.IsNaN(float64(*s.HybridAlpha)) || math.IsInf(float64(*s.HybridAlpha), 0) || *s.HybridAlpha < 0 || *s.HybridAlpha > 1) {
		return errors.New("typesense: HybridAlpha must be finite and in [0,1]")
	}
	if s.MaxResponseBytes < 0 || s.MaxResponseBytes == math.MaxInt64 {
		return errors.New("typesense: MaxResponseBytes must allow a positive bounded read")
	}
	return nil
}

var (
	_ vectorstore.Indexer   = (*Store)(nil)
	_ vectorstore.Searcher  = (*Store)(nil)
	_ vectorstore.IDDeleter = (*Store)(nil)
)

type Store struct {
	client           APIClient
	collectionName   string
	embeddingClient  embeddingclient.Client
	documentBatcher  vectorstore.Batcher
	schema           collectionSchema
	hybridAlpha      *float32
	maxResponseBytes int64
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	client, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, err
	}
	store := &Store{client: config.Client, collectionName: cmp.Or(config.CollectionName, DefaultCollectionName), embeddingClient: client, documentBatcher: config.DocumentBatcher, maxResponseBytes: cmp.Or(config.MaxResponseBytes, DefaultMaxResponseBytes)}
	if config.HybridAlpha != nil {
		store.hybridAlpha = new(*config.HybridAlpha)
	}
	native, callErr := store.client.GetCollection(ctx, store.collectionName)
	raw, err := store.readResponse(native, callErr)
	if err != nil {
		return nil, err
	}
	var schema api.CollectionResponse
	if err = jsonv2.Unmarshal(raw, &schema); err != nil {
		return nil, err
	}
	store.schema, err = newCollectionSchema(&schema, store.collectionName)
	if err != nil {
		return nil, err
	}
	if _, err = store.matchingKeys(ctx, nil); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) readResponse(response *http.Response, callErr error) (raw []byte, err error) {
	if response == nil {
		if callErr != nil {
			return nil, callErr
		}
		return nil, errors.New("typesense: native operation returned no response")
	}
	if lo.IsNil(response.Body) {
		return nil, errors.Join(callErr, errors.New("typesense: native response has no body"))
	}
	defer func() {
		err = errors.Join(err, response.Body.Close())
		if err != nil {
			raw = nil
		}
	}()
	if callErr != nil {
		return nil, callErr
	}
	raw, err = io.ReadAll(io.LimitReader(response.Body, s.maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > s.maxResponseBytes {
		return nil, errors.New("typesense: native response exceeds MaxResponseBytes")
	}
	if response.StatusCode != http.StatusOK {
		return nil, &nativesense.HTTPError{Status: response.StatusCode, Body: raw}
	}
	return raw, nil
}

func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	records := make(map[string]storedRecord, len(request.Documents))
	for _, doc := range request.Documents {
		record, err := encodeDocument(doc)
		if err != nil {
			return err
		}
		records[doc.ID] = record
	}
	batches, err := request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return err
	}
	var payloads [][]byte
	for _, batch := range batches {
		texts, textErr := batch.Texts()
		if textErr != nil {
			return textErr
		}
		vectors, embedErr := s.embeddingClient.EmbedTexts(ctx, texts)
		if embedErr != nil {
			return embedErr
		}
		var payload bytes.Buffer
		for i, doc := range batch.Documents {
			vector, vectorErr := s.schema.narrow(vectors[i])
			if vectorErr != nil {
				return vectorErr
			}
			record := records[doc.ID]
			record.Embedding = &vector
			body, encodeErr := jsonv2.Marshal(record)
			if encodeErr != nil {
				return encodeErr
			}
			payload.Write(body)
			payload.WriteByte('\n')
		}
		payloads = append(payloads, payload.Bytes())
	}
	for i, payload := range payloads {
		native, callErr := s.client.ImportDocumentsWithBody(ctx, s.collectionName, &api.ImportDocumentsParams{Action: new(api.Upsert)}, "application/octet-stream", bytes.NewReader(payload))
		raw, readErr := s.readResponse(native, callErr)
		if readErr != nil {
			return readErr
		}
		if ackErr := validateImport(raw, len(batches[i].Documents)); ackErr != nil {
			return ackErr
		}
	}
	return nil
}

func (s *Store) matchingKeys(ctx context.Context, predicate filter.Predicate) ([]string, error) {
	native, callErr := s.client.ExportDocuments(ctx, s.collectionName, nil)
	raw, err := s.readResponse(native, callErr)
	if err != nil {
		return nil, err
	}
	decoder := jsontext.NewDecoder(bytes.NewReader(raw))
	var keys []string
	seen := make(map[string]struct{})
	for {
		value, readErr := decoder.ReadValue()
		if errors.Is(readErr, io.EOF) {
			return keys, nil
		}
		if readErr != nil {
			return nil, readErr
		}
		record, doc, decodeErr := s.schema.decodeDocument(value)
		if decodeErr != nil {
			return nil, decodeErr
		}
		if _, duplicate := seen[*record.ID]; duplicate {
			return nil, errors.New("typesense: export repeated a native key")
		}
		seen[*record.ID] = struct{}{}
		if predicate != nil {
			values, valueErr := doc.Metadata.Values()
			if valueErr != nil {
				return nil, valueErr
			}
			matched, matchErr := filter.Match(predicate, values)
			if matchErr != nil {
				return nil, matchErr
			}
			if !matched {
				continue
			}
		}
		keys = append(keys, *record.ID)
	}
}

func (s *Store) Search(ctx context.Context, request *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	if err = request.Validate(); err != nil {
		return nil, err
	}
	if err = request.Options.RequireMode(vectorstore.SearchModeSemantic, vectorstore.SearchModeHybrid); err != nil {
		return nil, err
	}
	if request.Options.EffectiveMode() == vectorstore.SearchModeHybrid && request.Options.ResultLimit() > MaxResultsPerPage {
		return nil, fmt.Errorf("%w: typesense hybrid TopK must not exceed %d because fusion changes between pages", vectorstore.ErrInvalidOptions, MaxResultsPerPage)
	}
	defer func() {
		if err == nil {
			err = response.ValidateFor(request)
		}
		if err != nil {
			response = nil
		}
	}()
	keys, err := s.matchingKeys(ctx, request.Options.Filter)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return &vectorstore.SearchResponse{Results: []*vectorstore.SearchResult{}}, nil
	}
	vector, err := s.embeddingClient.EmbedText(ctx, request.Query)
	if err != nil {
		return nil, err
	}
	query, err := s.schema.narrow(vector)
	if err != nil {
		return nil, err
	}
	params := s.searchParameters(request, query)
	allowed := make(map[string]struct{}, len(keys))
	if request.Options.Filter != nil {
		params.FilterBy = new(keyFilter(keys))
		for _, key := range keys {
			allowed[key] = struct{}{}
		}
	}
	var results []*vectorstore.SearchResult
	seen := make(map[string]struct{})
	ranked := 0
	total := -1
	for page := 1; ; page++ {
		params.Page = new(page)
		body, encodeErr := jsonv2.Marshal(struct {
			Searches []searchQuery `json:"searches"`
		}{Searches: []searchQuery{*params}})
		if encodeErr != nil {
			return nil, encodeErr
		}
		native, callErr := s.client.MultiSearchWithBody(ctx, nil, "application/json", bytes.NewReader(body))
		raw, readErr := s.readResponse(native, callErr)
		if readErr != nil {
			return nil, readErr
		}
		var wire struct {
			Results []struct {
				Code   *int         `json:"code"`
				Error  *string      `json:"error"`
				Found  *int         `json:"found"`
				Hits   *[]searchHit `json:"hits"`
				Cutoff *bool        `json:"search_cutoff"`
			} `json:"results"`
		}
		if decodeErr := jsonv2.Unmarshal(raw, &wire); decodeErr != nil {
			return nil, decodeErr
		}
		if len(wire.Results) != 1 {
			return nil, errors.New("typesense: multi-search must return exactly one result")
		}
		output := wire.Results[0]
		if output.Error != nil || (output.Code != nil && *output.Code >= 400) {
			return nil, fmt.Errorf("typesense: native search failed: code %d: %s", lo.FromPtr(output.Code), lo.FromPtr(output.Error))
		}
		if output.Found == nil || *output.Found < 0 || output.Hits == nil || output.Cutoff == nil || *output.Cutoff {
			return nil, errors.New("typesense: native search omitted a complete result count, hit array or cutoff acknowledgment")
		}
		if total == -1 {
			total = *output.Found
		} else if total != *output.Found {
			return nil, errors.New("typesense: native result count changed during pagination")
		}
		for _, hit := range *output.Hits {
			record, doc, decodeErr := s.schema.decodeDocument(hit.Document)
			if decodeErr != nil {
				return nil, decodeErr
			}
			if _, duplicate := seen[*record.ID]; duplicate {
				return nil, errors.New("typesense: search repeated a native key")
			}
			seen[*record.ID] = struct{}{}
			if request.Options.Filter != nil {
				if _, member := allowed[*record.ID]; !member {
					return nil, errors.New("typesense: native hit is outside Core membership")
				}
				values, valueErr := doc.Metadata.Values()
				if valueErr != nil {
					return nil, valueErr
				}
				matched, matchErr := filter.Match(request.Options.Filter, values)
				if matchErr != nil {
					return nil, matchErr
				}
				if !matched {
					return nil, errors.New("typesense: native hit changed Core membership during search")
				}
			}
			score := vectorstore.Score(1 / float64(ranked+1))
			if hit.VectorDistance != nil {
				value := *hit.VectorDistance
				if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 2 {
					return nil, errors.New("typesense: native cosine distance is outside [0,2]")
				}
				if request.Options.EffectiveMode() == vectorstore.SearchModeSemantic {
					score = vectorstore.ScoreFromCosineDistance(value)
				}
			} else if request.Options.EffectiveMode() == vectorstore.SearchModeSemantic {
				return nil, errors.New("typesense: semantic hit has no vector distance")
			}
			match, resultErr := vectorstore.NewSearchResult(doc, score)
			if resultErr != nil {
				return nil, resultErr
			}
			ranked++
			if score >= request.Options.MinScore && ranked <= request.Options.ResultLimit() {
				results = append(results, match)
			}
		}
		expected := min(total, request.Options.ResultLimit())
		if ranked > expected {
			return nil, errors.New("typesense: native search returned more hits than its result budget")
		}
		if ranked == expected {
			return &vectorstore.SearchResponse{Results: results}, nil
		}
		if len(*output.Hits) < *params.PerPage {
			return nil, errors.New("typesense: native search ended before its reported result count")
		}
	}
}

func (s *Store) searchParameters(request *vectorstore.SearchRequest, vector []float32) *searchQuery {
	var alpha *float32
	if request.Options.EffectiveMode() == vectorstore.SearchModeHybrid {
		alpha = s.hybridAlpha
	}
	params := &searchQuery{MultiSearchCollectionParameters: api.MultiSearchCollectionParameters{Collection: new(s.collectionName), Q: new("*"), VectorQuery: new(formatVectorQuery(vector, request.Options.ResultLimit(), alpha)), PerPage: new(min(request.Options.ResultLimit(), MaxResultsPerPage))}}
	if request.Options.EffectiveMode() == vectorstore.SearchModeHybrid {
		params.Q = new(request.Query)
		params.QueryBy = new("content")
	}
	return params
}

func (s *Store) DeleteIDs(ctx context.Context, ids []string) error {
	var keys []string
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		key, err := encodeKey(id)
		if err != nil {
			return err
		}
		if _, duplicate := seen[key]; !duplicate {
			keys = append(keys, key)
			seen[key] = struct{}{}
		}
	}
	for group := range slices.Chunk(keys, maxKeysPerDelete) {
		native, callErr := s.client.DeleteDocuments(ctx, s.collectionName, &api.DeleteDocumentsParams{FilterBy: new(keyFilter(group))})
		raw, readErr := s.readResponse(native, callErr)
		if readErr != nil {
			return readErr
		}
		var ack struct {
			Count *int `json:"num_deleted"`
		}
		if err := jsonv2.Unmarshal(raw, &ack); err != nil {
			return err
		}
		if ack.Count == nil || *ack.Count < 0 || *ack.Count > len(group) {
			return errors.New("typesense: deletion returned an invalid acknowledgment count")
		}
	}
	return nil
}

func keyFilter(keys []string) string { return "id:=[" + strings.Join(keys, ",") + "]" }

func validateImport(raw []byte, want int) error {
	decoder := jsontext.NewDecoder(bytes.NewReader(raw))
	count := 0
	for {
		var ack struct {
			Success *bool   `json:"success"`
			Error   *string `json:"error"`
		}
		err := jsonv2.UnmarshalDecode(decoder, &ack)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if ack.Success == nil || !*ack.Success {
			return fmt.Errorf("typesense: import record %d failed: %s", count, lo.FromPtr(ack.Error))
		}
		count++
	}
	if count != want {
		return fmt.Errorf("typesense: import acknowledged %d of %d records", count, want)
	}
	return nil
}

type searchHit struct {
	Document       json.RawMessage `json:"document"`
	VectorDistance *float64        `json:"vector_distance"`
}

type searchQuery struct {
	api.MultiSearchCollectionParameters `json:",inline"`
	EnableCurations                     bool `json:"enable_curations"`
}

func formatVectorQuery(vector []float32, topK int, alpha *float32) string {
	var query strings.Builder
	query.WriteString("embedding:([")
	for i, value := range vector {
		if i > 0 {
			query.WriteByte(',')
		}
		query.WriteString(strconv.FormatFloat(float64(value), 'g', -1, 32))
	}
	query.WriteString("], k: ")
	query.WriteString(strconv.Itoa(topK))
	if alpha != nil {
		query.WriteString(", alpha: ")
		query.WriteString(strconv.FormatFloat(float64(*alpha), 'g', -1, 32))
	}
	query.WriteByte(')')
	return query.String()
}
