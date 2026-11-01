package weaviate

import (
	"cmp"
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	weaviateclient "github.com/weaviate/weaviate-go-client/v5/weaviate"
	"github.com/weaviate/weaviate/entities/models"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

type nativeFixture struct {
	t           *testing.T
	mu          sync.Mutex
	class       *models.Class
	objects     map[string]*models.Object
	batches     atomic.Int64
	deletes     atomic.Int64
	acknowledge func([]models.ObjectsGetResponse) []models.ObjectsGetResponse
	queryID     *string

	distances    map[string]float64
	hybridScores map[string]float64
	beforeQuery  func()
	queryChange  func([]map[string]any) []map[string]any

	groups      []int
	hybrids     []int
	scans       int
	queryError  bool
	sourceError bool
}

func (n *nativeFixture) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	writer.Header().Set("Content-Type", "application/json")
	var response any
	switch {
	case request.URL.Path == "/v1/meta":
		response = map[string]any{"version": "1.39.8"}
	case request.Method == http.MethodGet && request.URL.Path == "/v1/schema/Documents":
		response = n.class
	case request.Method == http.MethodGet && request.URL.Path == "/v1/objects":
		n.scans++
		http.Error(writer, "source enumeration not granted", http.StatusForbidden)
		return
	case request.Method == http.MethodPost && request.URL.Path == "/v1/batch/objects":
		n.batches.Add(1)
		var body struct {
			Objects []*models.Object `json:"objects"`
		}
		if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
			n.t.Error(err)
			http.Error(writer, "invalid objects", http.StatusBadRequest)
			return
		}
		var results []models.ObjectsGetResponse
		for _, object := range body.Objects {
			n.objects[object.ID.String()] = object
			results = append(results, models.ObjectsGetResponse{Object: *object, Result: batchResult(models.ObjectsGetResponseAO2ResultStatusSUCCESS, nil)})
		}
		if n.acknowledge != nil {
			results = n.acknowledge(results)
		}
		response = results
	case request.Method == http.MethodPost && request.URL.Path == "/v1/graphql":
		if n.queryError {
			http.Error(writer, "native query unavailable", http.StatusServiceUnavailable)
			return
		}
		var body struct {
			Query string `json:"query"`
		}
		if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
			n.t.Error(err)
			return
		}
		if strings.Contains(body.Query, "certainty:") {
			n.t.Error("native threshold hid strict result validation")
		}
		var terms []string
		if found := regexp.MustCompile(`valueText:\s*(\[[^\]]*\])`).FindStringSubmatch(body.Query); found != nil {
			if !strings.Contains(body.Query, fieldTerms) || !strings.Contains(body.Query, "ContainsAny") {
				n.t.Error("filter must select exact precomputed terms")
			}
			if err := jsonv2.Unmarshal([]byte(found[1]), &terms); err != nil {
				n.t.Fatal(err)
			}
			n.groups = append(n.groups, len(terms))
		}
		hybrid := strings.Contains(body.Query, "hybrid:")
		if hybrid {
			n.hybrids = append(n.hybrids, len(terms))
		}
		if n.beforeQuery != nil {
			n.beforeQuery()
			n.beforeQuery = nil
		}
		var items []map[string]any
		for _, id := range slices.Sorted(maps.Keys(n.objects)) {
			object := n.objects[id]
			properties := object.Properties.(map[string]any)
			if len(terms) != 0 {
				got, _ := properties[fieldTerms].([]any)
				if !slices.Contains(got, any(terms[0])) {
					continue
				}
			}
			nativeID := id
			if n.queryID != nil {
				nativeID = *n.queryID
			}
			additional := map[string]any{additionalID: nativeID, additionalVector: object.Vector}
			if hybrid {
				score := 1.0
				if value, ok := n.hybridScores[id]; ok {
					score = value
				}
				additional[additionalScore] = strconv.FormatFloat(score, 'g', -1, 64)
			} else {
				additional[additionalDistance] = n.distances[id]
			}
			items = append(items, map[string]any{fieldContent: properties[fieldContent], fieldMetadata: properties[fieldMetadata], fieldTerms: properties[fieldTerms], "_additional": additional})
		}
		if !hybrid {
			slices.SortFunc(items, func(left, right map[string]any) int {
				return cmp.Compare(left["_additional"].(map[string]any)[additionalDistance].(float64), right["_additional"].(map[string]any)[additionalDistance].(float64))
			})
		} else {
			slices.SortFunc(items, func(left, right map[string]any) int {
				leftScore, rightScore := 1.0, 1.0
				if score, ok := n.hybridScores[left["_additional"].(map[string]any)[additionalID].(string)]; ok {
					leftScore = score
				}
				if score, ok := n.hybridScores[right["_additional"].(map[string]any)[additionalID].(string)]; ok {
					rightScore = score
				}
				return cmp.Compare(rightScore, leftScore)
			})
		}
		found := regexp.MustCompile(`limit:\s*(\d+)`).FindStringSubmatch(body.Query)
		if found == nil {
			n.t.Error("native query lacks limit")
			return
		}
		limit, err := strconv.Atoi(found[1])
		if err != nil {
			n.t.Error(err)
			return
		}
		items = items[:min(len(items), limit)]
		if n.queryChange != nil {
			items = n.queryChange(items)
		}
		response = map[string]any{"data": map[string]any{"Get": map[string]any{"Documents": items}}}
	case request.Method == http.MethodDelete && strings.HasPrefix(request.URL.Path, "/v1/objects/Documents/"):
		n.deletes.Add(1)
		id := strings.TrimPrefix(request.URL.Path, "/v1/objects/Documents/")
		if _, ok := n.objects[id]; !ok {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		delete(n.objects, id)
		writer.WriteHeader(http.StatusNoContent)
		return
	default:
		n.t.Errorf("unexpected %s %s", request.Method, request.URL.Path)
		http.NotFound(writer, request)
		return
	}
	if err := jsonv2.MarshalWrite(writer, response); err != nil {
		n.t.Error(err)
	}
}

func nativeClass(name, metric string) *models.Class {
	return &models.Class{Class: name, Description: fixtureSpace.StorageKey(), Vectorizer: "none", VectorIndexType: "hnsw", VectorIndexConfig: map[string]any{"distance": metric}, Properties: []*models.Property{{Name: fieldContent, DataType: []string{"text"}, Tokenization: "word"}, {Name: fieldMetadata, DataType: []string{"text"}, Tokenization: "field"}, {Name: fieldTerms, DataType: []string{"text[]"}, Tokenization: "field", IndexFilterable: new(true), IndexSearchable: new(false)}}}
}

func newNativeStore(t *testing.T, fixture *nativeFixture) *Store {
	t.Helper()
	fixture.t, fixture.objects = t, make(map[string]*models.Object)
	if fixture.class == nil {
		fixture.class = nativeClass("Documents", distanceCosine)
	}
	server := httptest.NewServer(fixture)
	t.Cleanup(server.Close)
	client, err := weaviateclient.NewClient(weaviateclient.Config{Host: strings.TrimPrefix(server.URL, "http://"), Scheme: "http"})
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(t.Context(), StoreConfig{Client: client, ClassName: "Documents", Space: fixtureSpace})
	if err != nil {
		t.Fatal(fmt.Errorf("construct native store: %w", err))
	}
	return store
}

const fixtureSpace vectorstore.SpaceID = "paired-v1"

func fixtureVector(text string) []float64 { return []float64{1, 0} }
func fixtureBatch(docs []*document.Document) *vectorstore.Batch {
	b := &vectorstore.Batch{Space: fixtureSpace}
	for _, doc := range docs {
		b.Records = append(b.Records, vectorstore.Record{Document: doc, Values: fixtureVector(doc.Text)})
	}
	return b
}
func fixtureQuery(ctx context.Context, store *Store, q *vectorstore.Query) (*vectorstore.SearchResponse, error) {
	if q.Options.EffectiveMode() == vectorstore.SearchModeHybrid {
		q.LexicalText = "native text"
	}
	return store.Query(ctx, q)
}
