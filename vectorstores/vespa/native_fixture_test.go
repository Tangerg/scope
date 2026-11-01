package vespa

import (
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
)

const fixtureSpace vectorstore.SpaceID = "fixture.v1"

var fixtureBudget = vectorstore.ScanBudget{MaxRecords: 3000, MaxBytes: 64 << 20, Timeout: time.Minute}

func fixtureVector(text string) []float64 {
	if text == "ordinary" {
		return []float64{0, 1}
	}
	return []float64{1, 0}
}
func fixtureBatch(docs []*document.Document) *vectorstore.Batch {
	b := &vectorstore.Batch{Space: fixtureSpace}
	for _, d := range docs {
		b.Records = append(b.Records, vectorstore.Record{Document: d, Values: fixtureVector(d.Text)})
	}
	return b
}
func fixtureMeter(t *testing.T, ctx context.Context) *vectorstore.ScanMeter {
	t.Helper()
	m, err := fixtureBudget.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}
func fixtureNamespace() string {
	return (StoreConfig{Namespace: "scope", Space: fixtureSpace, Dimensions: 2}).DocumentNamespace()
}

type nativeFixture struct {
	mu             sync.Mutex
	rows           map[string]metadata.Map
	writes         int
	queries        int
	deletes        int
	pageSize       int
	beforeDelete   func(string, metadata.Map)
	searchResponse func([]string, []queryHit) any
	visitResponse  func(*http.Request) any
	putResponse    func(string) any
}

func newFixtureStore(t *testing.T, config StoreConfig) (*Store, *nativeFixture) {
	t.Helper()
	fixture := &nativeFixture{rows: make(map[string]metadata.Map), pageSize: 4}
	server := httptest.NewServer(http.HandlerFunc(fixture.serve))
	t.Cleanup(server.Close)
	config.Endpoint = server.URL
	config.SchemaName = "scope"
	config.Namespace = "scope"
	config.RankingProfile = "scope_rank"
	config.HTTPClient = server.Client()
	config.Space = fixtureSpace
	config.Dimensions = 2
	store, err := NewStore(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	return store, fixture
}

func (n *nativeFixture) serve(writer http.ResponseWriter, request *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	writer.Header().Set("Content-Type", "application/json")
	var response any
	if request.URL.Path == "/search/" {
		n.queries++
		var body struct {
			YQL    string `json:"yql"`
			Hits   int    `json:"hits"`
			Tensor struct {
				Values []float64 `json:"values"`
			} `json:"input.query(q)"`
		}
		if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
			http.Error(writer, err.Error(), 400)
			return
		}
		_, tail, ok := strings.Cut(body.YQL, " and "+identityAttribute+" in (")
		if !ok || !strings.HasSuffix(tail, ")") || body.Hits > queryGroupSize {
			http.Error(writer, "invalid ID query", 400)
			return
		}
		var ids []string
		if err := jsonv2.Unmarshal([]byte("["+strings.TrimSuffix(tail, ")")+"]"), &ids); err != nil {
			http.Error(writer, err.Error(), 400)
			return
		}
		hits := make([]queryHit, 0, len(ids))
		for _, rawID := range ids {
			id := strings.TrimPrefix(rawID, "id:"+fixtureNamespace()+":scope::")
			fields, exists := n.rows[id]
			if !exists {
				continue
			}
			tensor, _, err := fields.Decode[nativeTensor]("embedding")
			if err != nil || len(tensor.Values) != len(body.Tensor.Values) {
				http.Error(writer, "invalid native tensor", 400)
				return
			}
			squared := 0.0
			for index, value := range tensor.Values {
				difference := value - body.Tensor.Values[index]
				squared += difference * difference
			}
			score := 1 / (1 + math.Sqrt(squared))
			summary := fields.Clone()
			if err := summary.Set(nativeIDField, rawID); err != nil {
				http.Error(writer, err.Error(), 400)
				return
			}
			hits = append(hits, queryHit{Relevance: &score, Fields: summary})
		}
		response = struct {
			Root any `json:"root"`
		}{struct {
			Coverage queryCoverage `json:"coverage"`
			Children []queryHit    `json:"children"`
		}{queryCoverage{Coverage: 100, Full: true}, hits}}
		if n.searchResponse != nil {
			response = n.searchResponse(ids, hits)
		}
	} else {
		prefix := "/document/v1/" + fixtureNamespace() + "/scope/docid/"
		id, ok := strings.CutPrefix(request.URL.Path, prefix)
		if !ok {
			http.Error(writer, "unexpected native scope", 400)
			return
		}
		switch request.Method {
		case http.MethodGet:
			if id != "" {
				http.Error(writer, "unexpected single-document read", 400)
				return
			}
			if n.visitResponse != nil {
				response = n.visitResponse(request)
				break
			}
			start := 0
			if token := request.URL.Query().Get("continuation"); token != "" {
				var err error
				start, err = strconv.Atoi(strings.TrimPrefix(token, "opaque:"))
				if err != nil {
					http.Error(writer, err.Error(), 400)
					return
				}
			}
			ids := make([]string, 0, len(n.rows))
			for identity := range n.rows {
				ids = append(ids, identity)
			}
			slices.Sort(ids)
			end := min(start+n.pageSize, len(ids))
			page := visitResponse{DocumentCount: new(end - start), Documents: make([]visitDocument, 0, end-start)}
			for _, identity := range ids[start:end] {
				page.Documents = append(page.Documents, visitDocument{ID: "id:" + fixtureNamespace() + ":scope::" + identity, Fields: n.rows[identity]})
			}
			if end < len(ids) {
				page.Continuation = "opaque:" + strconv.Itoa(end)
			}
			response = page
		case http.MethodPost:
			n.writes++
			var body struct {
				Fields metadata.Map `json:"fields"`
			}
			if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
				http.Error(writer, err.Error(), 400)
				return
			}
			tensor, _, err := body.Fields.Decode[nativeTensor]("embedding")
			if err != nil {
				http.Error(writer, err.Error(), 400)
				return
			}
			tensor.Type = "tensor<float>(x[2])"
			if err := body.Fields.Set("embedding", tensor); err != nil {
				http.Error(writer, err.Error(), 400)
				return
			}
			n.rows[id] = body.Fields
			response = map[string]string{"id": "id:" + fixtureNamespace() + ":scope::" + id}
			if n.putResponse != nil {
				response = n.putResponse(id)
			}
		case http.MethodDelete:
			n.deletes++
			row := n.rows[id]
			if n.beforeDelete != nil {
				n.beforeDelete(id, row)
			}
			literal, ok := strings.CutPrefix(request.URL.Query().Get("condition"), "scope.scope_metadata == ")
			var observed string
			if !ok || jsonv2.Unmarshal([]byte(literal), &observed) != nil {
				http.Error(writer, "missing metadata condition", 400)
				return
			}
			current, present, err := row.Decode[string](metadataField)
			if err != nil || !present || current != observed {
				http.Error(writer, "condition failed", http.StatusPreconditionFailed)
				return
			}
			delete(n.rows, id)
			response = map[string]string{"id": "id:" + fixtureNamespace() + ":scope::" + id}
		default:
			http.Error(writer, "unexpected method", 400)
			return
		}
	}
	if err := jsonv2.MarshalWrite(writer, response); err != nil {
		panic(fmt.Sprintf("native fixture response: %v", err))
	}
}
