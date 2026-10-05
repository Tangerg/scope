package bedrockkb

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	nativeDocument "github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime/types"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

type protocolFixture struct {
	t      *testing.T
	mu     sync.Mutex
	pages  []string
	bodies []metadata.Map
	status int
	server *httptest.Server
}

func newProtocolFixture(t *testing.T, pages ...string) *protocolFixture {
	t.Helper()
	fixture := &protocolFixture{t: t, pages: pages, status: http.StatusOK}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (p *protocolFixture) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if request.Method != http.MethodPost || request.URL.Path != "/knowledgebases/KB12345678/retrieve" {
		p.t.Errorf("unexpected native request %s %s", request.Method, request.URL.Path)
	}
	var body metadata.Map
	if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
		p.t.Error(err)
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	p.bodies = append(p.bodies, body)
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(p.status)
	index := len(p.bodies) - 1
	if index >= len(p.pages) {
		p.t.Error("unexpected extra retrieval")
		return
	}
	fmt.Fprint(writer, p.pages[index])
}

func (p *protocolFixture) config() StoreConfig {
	return StoreConfig{Endpoint: p.server.URL, HTTPClient: p.server.Client(), KnowledgeBaseID: "KB12345678"}
}

func (p *protocolFixture) store() *Store {
	p.t.Helper()
	store, err := NewStore(p.t.Context(), p.config())
	if err != nil {
		p.t.Fatal(err)
	}
	if len(p.bodies) != 0 {
		p.t.Fatal("construction performed I/O")
	}
	return store
}

func resultJSON(id string, score float64) string {
	return fmt.Sprintf(`{"documentId":%q,"content":{"type":"TEXT","text":"chunk"},"score":%v}`, id, score)
}

func TestNativeRESTPreservesExactCoreMetadata(t *testing.T) {
	for _, source := range []string{`null`, `{}`, `{"n":9007199254740993,"nested":{"n":1.0000000000000000001},"huge":1e1000,"null":null}`} {
		t.Run(source, func(t *testing.T) {
			fixture := newProtocolFixture(t, fmt.Sprintf(`{"retrievalResults":[{"documentId":"source","content":{"type":"TEXT","text":"chunk"},"score":0.5,"metadata":%s}]}`, source))
			response, err := fixture.store().Search(t.Context(), &vectorstore.SearchRequest{Query: "query"})
			if err != nil {
				t.Fatal(err)
			}
			var expected metadata.Map
			if err = expected.UnmarshalJSON([]byte(source)); err != nil {
				t.Fatal(err)
			}
			got := response.Results[0].Document.Metadata
			if !expected.Equal(got) || (expected == nil) != (got == nil) {
				t.Fatalf("native metadata = %v, want %v", got, expected)
			}
			if len(got) != 0 {
				values, decodeErr := got.Values()
				if decodeErr != nil {
					t.Fatal(decodeErr)
				}
				equal, matchErr := filter.Match(filter.EQ("n", int64(9007199254740993)), values)
				if matchErr != nil || !equal {
					t.Fatalf("numeric fact changed kind or digits: %v, %v", equal, matchErr)
				}
			}
		})
	}
}

func TestCorePredicatesAndNativeResultLimitsFailBeforeIO(t *testing.T) {
	fixture := newProtocolFixture(t)
	store := fixture.store()
	for _, predicate := range []filter.Predicate{filter.EQ("n", 1), filter.Like("n", "%x%"), filter.IsNull("n"), filter.Not(filter.LT("n", 2))} {
		response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: predicate}})
		if response != nil || !errors.Is(err, errors.ErrUnsupported) {
			t.Fatalf("unsupported predicate reached native semantics: %v, %v", response, err)
		}
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 101}})
	if response != nil || !errors.Is(err, vectorstore.ErrInvalidOptions) {
		t.Fatalf("native count was silently capped: %v, %v", response, err)
	}
	if len(fixture.bodies) != 0 {
		t.Fatal("invalid policy reached native HTTP")
	}
}

func TestPagingOwnsCompletionAndPreflightBeforeTopK(t *testing.T) {
	for _, test := range []string{"short pages", "empty advancing page", "bad late result", "repeated token", "missing array", "transport error"} {
		t.Run(test, func(t *testing.T) {
			first := fmt.Sprintf(`{"retrievalResults":[%s],"nextToken":"next"}`, resultJSON("one", .5))
			second := fmt.Sprintf(`{"retrievalResults":[%s]}`, resultJSON("two", .7))
			switch test {
			case "empty advancing page":
				first = `{"retrievalResults":[],"nextToken":"next"}`
			case "bad late result":
				second = `{"retrievalResults":[{"documentId":"bad","score":1}]}`
			case "repeated token":
				second = fmt.Sprintf(`{"retrievalResults":[%s],"nextToken":"next"}`, resultJSON("two", .7))
			case "missing array":
				first = `{"nextToken":"next"}`
			}
			fixture := newProtocolFixture(t, first, second)
			if test == "transport error" {
				fixture.status = http.StatusForbidden
			}
			response, err := fixture.store().Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1}})
			if test == "short pages" || test == "empty advancing page" {
				if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != "two" {
					t.Fatalf("native pages = %v, %v", response, err)
				}
				fixture.mu.Lock()
				defer fixture.mu.Unlock()
				if len(fixture.bodies) != 2 || string(fixture.bodies[1]["nextToken"]) != `"next"` {
					t.Fatalf("continuation bodies = %v", fixture.bodies)
				}
			} else if err == nil || response != nil {
				t.Fatalf("bad late native response became success: %v, %v", response, err)
			}
		})
	}
}

func TestSearchRanksNativeScoresBeforeClampingAndThreshold(t *testing.T) {
	page := fmt.Sprintf(`{"retrievalResults":[%s,%s,%s]}`, resultJSON("lower", 2), resultJSON("higher", 3), resultJSON("negative", -3))
	fixture := newProtocolFixture(t, page)
	response, err := fixture.store().Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1, MinScore: 1}})
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != "higher" || response.Results[0].Score != 1 {
		t.Fatalf("native rank = %v, %v", response, err)
	}
}

func TestRequestOwnsRerankingAndRetrievalCounts(t *testing.T) {
	fixture := newProtocolFixture(t, `{"retrievalResults":[]}`)
	config := fixture.config()
	config.RerankingModelConfiguration = &types.VectorSearchBedrockRerankingModelConfiguration{ModelArn: aws.String("arn:reranker"), AdditionalModelRequestFields: map[string]nativeDocument.Interface{"temperature": nativeDocument.NewLazyDocument(.2)}}
	config.RerankingMetadataConfiguration = &types.MetadataConfigurationForReranking{SelectionMode: types.RerankingMetadataSelectionModeSelective, SelectiveModeConfiguration: &types.RerankingMetadataSelectiveModeConfigurationMemberFieldsToInclude{Value: []types.FieldForReranking{{FieldName: aws.String("title")}}}}
	config.ImplicitFilterConfiguration = &types.ImplicitFilterConfiguration{ModelArn: aws.String("arn:implicit"), MetadataAttributes: []types.MetadataAttributeSchema{{Key: aws.String("tenant"), Type: types.AttributeTypeString, Description: aws.String("tenant name")}}}
	store, err := NewStore(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	*config.RerankingModelConfiguration.ModelArn = "changed"
	config.RerankingModelConfiguration.AdditionalModelRequestFields["temperature"] = nativeDocument.NewLazyDocument(99)
	config.RerankingMetadataConfiguration.SelectiveModeConfiguration.(*types.RerankingMetadataSelectiveModeConfigurationMemberFieldsToInclude).Value[0].FieldName = aws.String("changed")
	*config.ImplicitFilterConfiguration.ModelArn = "changed"
	*config.ImplicitFilterConfiguration.MetadataAttributes[0].Key = "changed"
	if _, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 7, Mode: vectorstore.SearchModeHybrid}}); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	var wire struct {
		Query struct {
			Text string `json:"text"`
		} `json:"retrievalQuery"`
		Configuration struct {
			Vector struct {
				Count     int             `json:"numberOfResults"`
				Mode      string          `json:"overrideSearchType"`
				Filter    json.RawMessage `json:"filter"`
				Reranking struct {
					Type    string `json:"type"`
					Bedrock struct {
						Count int `json:"numberOfRerankedResults"`
						Model struct {
							ARN    string       `json:"modelArn"`
							Fields metadata.Map `json:"additionalModelRequestFields"`
						} `json:"modelConfiguration"`
						Metadata struct {
							Mode      string `json:"selectionMode"`
							Selective struct {
								Included []struct {
									Name string `json:"fieldName"`
								} `json:"fieldsToInclude"`
							} `json:"selectiveModeConfiguration"`
						} `json:"metadataConfiguration"`
					} `json:"bedrockRerankingConfiguration"`
				} `json:"rerankingConfiguration"`
				Implicit struct {
					ARN        string `json:"modelArn"`
					Attributes []struct {
						Key         string `json:"key"`
						Type        string `json:"type"`
						Description string `json:"description"`
					} `json:"metadataAttributes"`
				} `json:"implicitFilterConfiguration"`
			} `json:"vectorSearchConfiguration"`
		} `json:"retrievalConfiguration"`
	}
	encoded, err := fixture.bodies[0].MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if err = jsonv2.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	vector := wire.Configuration.Vector
	if wire.Query.Text != "query" || vector.Count != 7 || vector.Reranking.Bedrock.Count != 7 || vector.Mode != "HYBRID" || len(vector.Filter) != 0 {
		t.Fatalf("competing request policy = %+v", wire)
	}
	if vector.Reranking.Bedrock.Model.ARN != "arn:reranker" || string(vector.Reranking.Bedrock.Model.Fields["temperature"]) != "0.2" || vector.Reranking.Bedrock.Metadata.Selective.Included[0].Name != "title" || vector.Implicit.ARN != "arn:implicit" || vector.Implicit.Attributes[0].Key != "tenant" {
		t.Fatalf("native policy projection = %+v", wire)
	}
}

func TestNativeSourceIdentityAndInvalidRecords(t *testing.T) {
	for _, test := range []struct {
		name, location string
		valid          bool
	}{
		{"S3", `{"type":"S3","s3Location":{"uri":"s3://bucket/doc"}}`, true},
		{"Web", `{"type":"WEB","webLocation":{"url":"https://source/doc"}}`, true},
		{"OneDrive", `{"type":"ONEDRIVE","oneDriveLocation":{"url":"https://source/doc"}}`, true},
		{"Google Drive", `{"type":"GOOGLEDRIVE","googleDriveLocation":{"url":"https://source/doc"}}`, true},
		{"Custom", `{"type":"CUSTOM","customDocumentLocation":{"id":"opaque"}}`, true},
		{"SQL query", `{"type":"SQL","sqlLocation":{"query":"select * from t"}}`, false},
		{"missing source", `{"type":"S3"}`, false},
		{"mismatched source", `{"type":"S3","webLocation":{"url":"https://source/doc"}}`, false},
		{"empty source", `{}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			page := fmt.Sprintf(`{"retrievalResults":[{"content":{"type":"TEXT","text":"chunk"},"score":0.5,"location":%s}]}`, test.location)
			response, err := newProtocolFixture(t, page).store().Search(t.Context(), &vectorstore.SearchRequest{Query: "query"})
			if (err == nil) != test.valid || (err != nil && response != nil) {
				t.Fatalf("source identity = %v, %v", response, err)
			}
			if test.valid {
				var expected metadata.Map
				if err = expected.UnmarshalJSON([]byte(test.location)); err != nil {
					t.Fatal(err)
				}
				canonical, encodeErr := expected.MarshalJSON()
				if encodeErr != nil {
					t.Fatal(encodeErr)
				}
				if response.Results[0].Document.ID != string(canonical) {
					t.Fatalf("native identity changed: %q, want %s", response.Results[0].Document.ID, canonical)
				}
			}
		})
	}
	for _, row := range []string{`{"documentId":"one","content":{"type":"IMAGE","text":"wrong"},"score":0.5}`, `{"documentId":"one","content":{"type":"TEXT","text":"chunk"}}`, `{"documentId":"one","content":{"type":"TEXT","text":"chunk"},"score":0.5,"metadata":{"bad":}}`} {
		response, err := newProtocolFixture(t, `{"retrievalResults":[`+row+`]}`).store().Search(t.Context(), &vectorstore.SearchRequest{Query: "query"})
		if err == nil || response != nil {
			t.Fatalf("invalid native record = %v, %v", response, err)
		}
	}
}

func TestConfigAndNativeReadBudget(t *testing.T) {
	fixture := newProtocolFixture(t, strings.Repeat("x", 32))
	config := fixture.config()
	config.MaxResponseBytes = 4
	store, err := NewStore(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query"})
	if err == nil || response != nil || !strings.Contains(err.Error(), "4-byte limit") {
		t.Fatalf("read budget = %v, %v", response, err)
	}
	for _, change := range []func(*StoreConfig){
		func(config *StoreConfig) { config.HTTPClient = nil }, func(config *StoreConfig) { config.Endpoint = "https://service/path" },
		func(config *StoreConfig) { config.MaxResponseBytes = math.MaxInt64 }, func(config *StoreConfig) { config.KnowledgeBaseID = " " },
		func(config *StoreConfig) {
			config.RerankingModelConfiguration = &types.VectorSearchBedrockRerankingModelConfiguration{}
		},
		func(config *StoreConfig) {
			config.RerankingMetadataConfiguration = &types.MetadataConfigurationForReranking{}
		},
	} {
		config = fixture.config()
		change(&config)
		if store, err = NewStore(t.Context(), config); err == nil || store != nil {
			t.Fatal("invalid config accepted")
		}
	}
}

func TestCancellationAndNativeTransportFailures(t *testing.T) {
	fixture := newProtocolFixture(t)
	store := fixture.store()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if response, err := store.Search(ctx, &vectorstore.SearchRequest{Query: "query"}); response != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled query = %v, %v", response, err)
	}
	for _, raw := range []float64{math.NaN(), math.Inf(1)} {
		record := retrievalResult{Score: &raw}
		if _, err := record.match(); err == nil {
			t.Fatal("invalid native score accepted")
		}
	}
	if len(fixture.bodies) != 0 {
		t.Fatal("canceled request reached native server")
	}
}

func TestSearchKeepsMultipleNativeSourceChunks(t *testing.T) {
	page := fmt.Sprintf(`{"retrievalResults":[%s,%s]}`, resultJSON("source", .5), resultJSON("source", .6))
	response, err := newProtocolFixture(t, page).store().Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 2}})
	if err != nil || len(response.Results) != 2 {
		t.Fatalf("source chunks collapsed: %v, %v", response, err)
	}
	scores := []float64{response.Results[0].Score.Float64(), response.Results[1].Score.Float64()}
	if !slices.Equal(scores, []float64{.6, .5}) {
		t.Fatalf("native chunk rank = %v", scores)
	}
}
