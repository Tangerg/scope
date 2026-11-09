package typesense

import (
	"bytes"
	"cmp"
	"context"
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

	"github.com/Tangerg/scope/core/document"
	corevector "github.com/Tangerg/scope/core/vector"
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
	Space            vectorstore.SpaceID
	HybridAlpha      *float32
	MaxResponseBytes int64
}

func (s StoreConfig) Validate() error {
	if lo.IsNil(s.Client) {
		return errors.New("typesense: Client is required")
	}
	if err := s.Space.Validate(); err != nil {
		return err
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
	_ vectorstore.Upserter  = (*Store)(nil)
	_ vectorstore.Querier   = (*Store)(nil)
	_ vectorstore.IDDeleter = (*Store)(nil)
)

type Store struct {
	client           APIClient
	collectionName   string
	space            vectorstore.SpaceID
	schema           collectionSchema
	hybridAlpha      *float32
	maxResponseBytes int64
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	store := &Store{client: config.Client, collectionName: cmp.Or(config.CollectionName, DefaultCollectionName), space: config.Space, maxResponseBytes: cmp.Or(config.MaxResponseBytes, DefaultMaxResponseBytes)}
	if config.HybridAlpha != nil {
		store.hybridAlpha = new(*config.HybridAlpha)
	}
	schema, err := store.readSchema(ctx)
	if err != nil {
		return nil, err
	}
	store.schema = schema
	return store, nil
}

func (s *Store) readSchema(ctx context.Context) (collectionSchema, error) {
	native, callErr := s.client.GetCollection(ctx, s.collectionName)
	raw, err := s.readResponse(native, callErr)
	if err != nil {
		return collectionSchema{}, err
	}
	var schema api.CollectionResponse
	if err = jsonv2.Unmarshal(raw, &schema); err != nil {
		return collectionSchema{}, err
	}
	return newCollectionSchema(&schema, s.collectionName, s.space)
}
func (s *Store) checkBinding(ctx context.Context) error {
	schema, err := s.readSchema(ctx)
	if err != nil {
		return err
	}
	if schema != s.schema {
		return ErrIncompatibleCollection
	}
	return nil
}

func (s *Store) readResponse(response *http.Response, callErr error) ([]byte, error) {
	return readNativeResponse(response, callErr, s.maxResponseBytes)
}

var errResponseLimit = errors.New("typesense: native response exceeds MaxResponseBytes")

func readNativeResponse(response *http.Response, callErr error, limit int64) (raw []byte, err error) {
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
	}()
	raw, err = io.ReadAll(io.LimitReader(response.Body, limit+1))
	err = errors.Join(callErr, err)
	if response.StatusCode == 0 && callErr != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		err = errors.Join(err, errResponseLimit)
	}
	if response.StatusCode != http.StatusOK {
		return nil, errors.Join(err, &nativesense.HTTPError{Status: response.StatusCode, Body: raw})
	}
	return raw, err
}

func (s *Store) CheckDocuments(ctx context.Context, space vectorstore.SpaceID, docs []*document.Document) error {
	if err := space.Require(s.space); err != nil {
		return err
	}
	if err := vectorstore.ValidateDocuments(docs); err != nil {
		return err
	}
	for _, doc := range docs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := encodeDocument(doc); err != nil {
			return err
		}
	}
	return s.checkBinding(ctx)
}

func (s *Store) Upsert(ctx context.Context, batch *vectorstore.Batch) error {
	if err := batch.ValidateFor(s.space); err != nil {
		return err
	}
	var payload bytes.Buffer
	ids := make([]string, len(batch.Records))
	for i, r := range batch.Records {
		if err := ctx.Err(); err != nil {
			return err
		}
		item, err := encodeDocument(r.Document)
		if err != nil {
			return err
		}
		vec, err := s.schema.narrow(r.Values)
		if err != nil {
			return err
		}
		item.Embedding = new(corevector.Coordinates[float32](vec))
		wire, err := jsonv2.Marshal(item)
		if err != nil {
			return err
		}
		payload.Write(wire)
		payload.WriteByte('\n')
		ids[i] = r.Document.ID
	}
	if err := s.checkBinding(ctx); err != nil {
		return err
	}
	res, sendErr := s.client.ImportDocumentsWithBody(ctx, s.collectionName, &api.ImportDocumentsParams{Action: new(api.Upsert), ReturnId: new(true)}, mediaTypeJSONL, &payload)
	raw, err := s.readResponse(res, sendErr)
	return validateImport(raw, ids, err)
}

func (s *Store) matchingKeys(ctx context.Context, predicate filter.Predicate, budget vectorstore.ScanBudget) ([]string, error) {
	native, callErr := s.client.ExportDocuments(ctx, s.collectionName, nil)
	raw, err := readNativeResponse(native, callErr, min(budget.MaxBytes, s.maxResponseBytes))
	if errors.Is(err, errResponseLimit) && budget.MaxBytes <= s.maxResponseBytes {
		return nil, errors.Join(vectorstore.ErrScanBudget, err)
	}
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > budget.MaxBytes {
		return nil, vectorstore.ErrScanBudget
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
		if len(seen) > budget.MaxRecords {
			return nil, vectorstore.ErrScanBudget
		}
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

func (s *Store) Query(ctx context.Context, q *vectorstore.Query) (*vectorstore.SearchResponse, error) {
	return s.rank(ctx, q, nil)
}

// QueryByScan explicitly authorizes an export of the entire collection to compute
// exact membership. MaxBytes bounds the JSONL source payload, not ANN pages.
// The collection can change between export and ranking; membership is rechecked.
func (s *Store) QueryByScan(ctx context.Context, q *vectorstore.Query, budget vectorstore.ScanBudget) (*vectorstore.SearchResponse, error) {
	if err := budget.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, budget.Timeout)
	defer cancel()
	return s.rank(ctx, q, &budget)
}
func (s *Store) checkOptions(opts vectorstore.SearchOptions, allowFilter bool) error {
	if err := opts.Validate(); err != nil {
		return err
	}
	if err := opts.RequireMode(vectorstore.SearchModeSemantic, vectorstore.SearchModeHybrid); err != nil {
		return err
	}
	if opts.Mode == vectorstore.SearchModeHybrid && opts.ResultLimit() > MaxResultsPerPage {
		return fmt.Errorf("%w: typesense hybrid TopK must not exceed %d because fusion changes between pages", vectorstore.ErrInvalidOptions, MaxResultsPerPage)
	}
	if opts.Filter != nil && !allowFilter {
		return vectorstore.ErrUnsupportedFilter
	}
	return nil
}
func (s *Store) CheckQuery(ctx context.Context, space vectorstore.SpaceID, opts vectorstore.SearchOptions) error {
	if err := space.Require(s.space); err != nil {
		return err
	}
	if err := s.checkOptions(opts, false); err != nil {
		return err
	}
	return s.checkBinding(ctx)
}
func (s *Store) rank(ctx context.Context, request *vectorstore.Query, budget *vectorstore.ScanBudget) (response *vectorstore.SearchResponse, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = request.ValidateFor(s.space); err != nil {
		return nil, err
	}
	if err = s.checkOptions(request.Options, budget != nil); err != nil {
		return nil, err
	}
	query, err := s.schema.narrow(request.Values)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err == nil {
			err = request.ValidateResponse(response)
		}
		if err != nil {
			response = nil
		}
	}()

	if err = s.checkBinding(ctx); err != nil {
		return nil, err
	}
	var keys []string
	if request.Options.Filter != nil {
		if budget == nil {
			return nil, vectorstore.ErrUnsupportedFilter
		}
		keys, err = s.matchingKeys(ctx, request.Options.Filter, *budget)
		if err != nil {
			return nil, err
		}
		if len(keys) == 0 {
			return &vectorstore.SearchResponse{Results: []*vectorstore.SearchResult{}}, nil
		}
	}
	session := newSearchSession(s.schema, request.Options, *s.searchParameters(request, query), keys)
	for {
		body, encodeErr := session.requestBody()
		if encodeErr != nil {
			return nil, encodeErr
		}
		native, callErr := s.client.MultiSearchWithBody(ctx, nil, mediaTypeJSON, bytes.NewReader(body))
		raw, readErr := s.readResponse(native, callErr)
		if readErr != nil {
			return nil, readErr
		}
		done, pageErr := session.admitPage(raw)
		if pageErr != nil {
			return nil, pageErr
		}
		if done {
			return &vectorstore.SearchResponse{Results: session.results}, nil
		}
	}
}

func (s *Store) searchParameters(request *vectorstore.Query, vector []float32) *searchQuery {
	var alpha *float32
	if request.Options.Mode == vectorstore.SearchModeHybrid {
		alpha = s.hybridAlpha
	}
	params := &searchQuery{MultiSearchCollectionParameters: api.MultiSearchCollectionParameters{Collection: new(s.collectionName), Q: new(wildcardQuery), VectorQuery: new(formatVectorQuery(vector, request.Options.ResultLimit(), alpha)), PerPage: new(min(request.Options.ResultLimit(), MaxResultsPerPage))}}
	if request.Options.Mode == vectorstore.SearchModeHybrid {
		params.Q = new(request.LexicalText)
		params.QueryBy = new(fieldContent)
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
	if len(keys) == 0 {
		return nil
	}
	if err := s.checkBinding(ctx); err != nil {
		return err
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

func keyFilter(keys []string) string {
	return fieldID + keyFilterOperator + strings.Join(keys, ",") + "]"
}

type searchQuery struct {
	api.MultiSearchCollectionParameters `json:",inline"`
	EnableCurations                     bool `json:"enable_curations"`
}

func formatVectorQuery(vector []float32, topK int, alpha *float32) string {
	var query strings.Builder
	query.WriteString(fieldEmbedding + vectorQueryStart)
	for i, value := range vector {
		if i > 0 {
			query.WriteByte(',')
		}
		query.WriteString(strconv.FormatFloat(float64(value), 'g', -1, 32))
	}
	query.WriteString(vectorQueryNeighbors)
	query.WriteString(strconv.Itoa(topK))
	if alpha != nil {
		query.WriteString(vectorQueryAlpha)
		query.WriteString(strconv.FormatFloat(float64(*alpha), 'g', -1, 32))
	}
	query.WriteByte(')')
	return query.String()
}
