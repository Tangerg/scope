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

	"github.com/go-openapi/strfmt"
	weaviateclient "github.com/weaviate/weaviate-go-client/v5/weaviate"
	"github.com/weaviate/weaviate/entities/models"

	"github.com/Tangerg/scope/core/embedding"
)

type nativeFixture struct {
	t            *testing.T
	mu           sync.Mutex
	class        *models.Class
	objects      map[string]*models.Object
	batches      atomic.Int64
	deletes      atomic.Int64
	acknowledge  func([]models.ObjectsGetResponse) []models.ObjectsGetResponse
	queryID      *string
	sourceID     *string
	distances    map[string]float64
	hybridScores map[string]float64
	beforeQuery  func()
	beforeDelete func()
	queryChange  func([]map[string]any) []map[string]any
	sourceChange func([]*models.Object) []*models.Object
	deleteChange func(*models.BatchDeleteResponse)
	shortPages   bool
	groups       []int
	hybrids      []int
	scans        int
	queryError   bool
	sourceError  bool
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
		if n.sourceError {
			http.Error(writer, "native source unavailable", http.StatusServiceUnavailable)
			return
		}
		if request.URL.Query().Get("class") != "Documents" || request.URL.Query().Get("include") != "vector" {
			n.t.Error("enumeration lost class or dense vector")
		}
		limit, err := strconv.Atoi(request.URL.Query().Get("limit"))
		if err != nil {
			n.t.Error(err)
			return
		}
		if n.shortPages {
			limit = 1
		}
		var objects []*models.Object
		for _, id := range slices.Sorted(maps.Keys(n.objects)) {
			if id <= request.URL.Query().Get("after") {
				continue
			}
			object := *n.objects[id]
			if n.sourceID != nil {
				object.ID = strfmt.UUID(*n.sourceID)
			}
			objects = append(objects, &object)
			if len(objects) == limit {
				break
			}
		}
		if n.sourceChange != nil {
			objects = n.sourceChange(objects)
		}
		response = map[string]any{"objects": objects}
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
		var ids []string
		if found := regexp.MustCompile(`valueText:\s*(\[[^\]]*\])`).FindStringSubmatch(body.Query); found != nil {
			if err := jsonv2.Unmarshal([]byte(found[1]), &ids); err != nil {
				n.t.Error(err)
				return
			}
			n.groups = append(n.groups, len(ids))
		}
		hybrid := strings.Contains(body.Query, "hybrid:")
		if hybrid {
			n.hybrids = append(n.hybrids, len(ids))
		}
		if n.beforeQuery != nil {
			n.beforeQuery()
			n.beforeQuery = nil
		}
		var items []map[string]any
		for _, id := range slices.Sorted(maps.Keys(n.objects)) {
			if ids != nil && !slices.Contains(ids, id) {
				continue
			}
			object := n.objects[id]
			properties := object.Properties.(map[string]any)
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
			items = append(items, map[string]any{fieldContent: properties[fieldContent], fieldMetadata: properties[fieldMetadata], "_additional": additional})
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
	case request.Method == http.MethodDelete && request.URL.Path == "/v1/batch/objects":
		n.deletes.Add(1)
		var body models.BatchDelete
		if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
			n.t.Error(err)
			return
		}
		if body.Match == nil || body.Match.Class != "Documents" || body.Match.Where == nil || body.Match.Where.Operator != "And" || len(body.Match.Where.Operands) != 2 {
			n.t.Error("native deletion lost metadata precondition")
			return
		}
		identity, facts := body.Match.Where.Operands[0], body.Match.Where.Operands[1]
		if identity.Operator != "ContainsAny" || !slices.Equal(identity.Path, []string{"id"}) || facts.Operator != "Equal" || !slices.Equal(facts.Path, []string{fieldMetadata}) || facts.ValueText == nil {
			n.t.Error("native deletion has invalid where operands")
			return
		}
		if n.beforeDelete != nil {
			n.beforeDelete()
			n.beforeDelete = nil
		}
		results := &models.BatchDeleteResponseResults{Limit: 10000}
		for _, id := range identity.ValueTextArray {
			object, ok := n.objects[id]
			if !ok {
				continue
			}
			current := object.Properties.(map[string]any)[fieldMetadata].(string)
			if strings.TrimSpace(current) != strings.TrimSpace(*facts.ValueText) {
				continue
			}
			results.Objects = append(results.Objects, &models.BatchDeleteResponseResultsObjectsItems0{ID: strfmt.UUID(id), Status: new(models.BatchDeleteResponseResultsObjectsItems0StatusSUCCESS)})
			results.Matches++
			results.Successful++
			delete(n.objects, id)
		}
		answer := &models.BatchDeleteResponse{Match: &models.BatchDeleteResponseMatch{Class: "Documents"}, DryRun: new(false), Results: results}
		if n.deleteChange != nil {
			n.deleteChange(answer)
		}
		response = answer
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
	return &models.Class{Class: name, Vectorizer: "none", VectorIndexType: "hnsw", VectorIndexConfig: map[string]any{"distance": metric}, Properties: []*models.Property{{Name: fieldContent, DataType: []string{"text"}, Tokenization: "word"}, {Name: fieldMetadata, DataType: []string{"text"}, Tokenization: "field"}}}
}

func newNativeStore(t *testing.T, fixture *nativeFixture) (*Store, *atomic.Int64) {
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
	var calls atomic.Int64
	model := embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		calls.Add(1)
		outputs := make([]*embedding.Output, len(request.Texts))
		for i := range outputs {
			outputs[i] = &embedding.Output{Embedding: []float64{1, 0}}
		}
		return embedding.NewResponse(outputs, nil)
	})
	store, err := NewStore(t.Context(), StoreConfig{Client: client, ClassName: "Documents", EmbeddingModel: model, DocumentBatcher: testBatcher{}})
	if err != nil {
		t.Fatal(fmt.Errorf("construct native store: %w", err))
	}
	return store, &calls
}
