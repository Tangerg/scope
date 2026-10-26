package generator

import (
	"bytes"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	modelcatalog "github.com/Tangerg/scope/models/catalog"
)

func TestGeneratorPreservesKnownFreePricing(t *testing.T) {
	pricing, err := (&apiCost{Input: new(0.0), Output: new(0.0)}).pricing()
	if err != nil || len(pricing) != 1 {
		t.Fatalf("explicitly free input and output prices became unknown: %#v", pricing)
	}
}

type generatorFixture struct {
	directory     string
	api           map[string]apiProvider
	ladders       []catwalkProvider
	augmentations map[string]map[string]augEntry
}

func newGeneratorFixture(t *testing.T) *generatorFixture {
	t.Helper()
	fixture := &generatorFixture{
		directory: t.TempDir(), api: make(map[string]apiProvider),
		augmentations: map[string]map[string]augEntry{"openai": {"model": {Levels: []string{"low", "high"}, DefaultLevel: "low"}}},
	}
	for upstream := range providerMap {
		fixture.api[upstream] = apiProvider{Models: map[string]apiModel{"model": {
			ID: "model", Name: "Model", Reasoning: true,
			Cost: &apiCost{Input: new(1.0), Output: new(2.0)},
		}}}
	}
	fixture.api["togetherai"] = apiProvider{Models: map[string]apiModel{
		"essentialai/Rnj-1-Instruct": {ID: "essentialai/Rnj-1-Instruct"},
	}}
	for _, provider := range catwalkProviderMap {
		models := []catwalkModel{{ID: "model", ReasoningLevels: []string{"medium", "high"}, DefaultEffort: "high"}}
		if provider == "openai" {
			models = []catwalkModel{{ID: "model"}}
		}
		fixture.ladders = append(fixture.ladders, catwalkProvider{ID: provider, Models: models})
	}
	if err := os.Mkdir(filepath.Join(fixture.directory, "configs"), 0o755); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (g *generatorFixture) write(t *testing.T) Config {
	t.Helper()
	config := Config{Source: filepath.Join(g.directory, "source.json"), Ladders: filepath.Join(g.directory, "ladders.json"), Directory: g.directory}
	for path, data := range map[string]any{
		config.Source: g.api, config.Ladders: g.ladders, filepath.Join(g.directory, "augmentations.json"): g.augmentations,
	} {
		if err := writeJSON(path, data); err != nil {
			t.Fatal(err)
		}
	}
	return config
}

func (g *generatorFixture) provider(t *testing.T, name string) providerConfig {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(g.directory, "configs", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var provider providerConfig
	if err := jsonv2.Unmarshal(raw, &provider); err != nil {
		t.Fatal(err)
	}
	return provider
}

func TestGenerateOwnsEachProvidersFactsAndPreservesPricingPresence(t *testing.T) {
	fixture := newGeneratorFixture(t)
	fixture.api["openai"].Models["free"] = apiModel{ID: "free", Cost: &apiCost{Input: new(0.0), Output: new(0.0), CacheRead: new(0.0)}}
	fixture.api["openai"].Models["unknown"] = apiModel{ID: "unknown"}
	fixture.api["openai"].Models["embedding-model"] = apiModel{ID: "embedding-model"}
	fixture.api["openai"].Models["family-filtered"] = apiModel{ID: "family-filtered", Family: "embed"}
	fixture.api["openai"].Models["rerank-model"] = apiModel{ID: "rerank-model"}
	audio := apiModel{ID: "speech"}
	audio.Modalities.Output = []string{"audio"}
	fixture.api["openai"].Models[audio.ID] = audio
	vertex := apiModel{ID: "model@default", Reasoning: true, Cost: &apiCost{Input: new(3.0), Output: new(4.0)}}
	fixture.api["google-vertex"] = apiProvider{Models: map[string]apiModel{vertex.ID: vertex}}
	for index := range fixture.ladders {
		if fixture.ladders[index].ID == "vertexai" {
			fixture.ladders[index].Models[0].DefaultEffort = "medium"
		}
	}
	config := fixture.write(t)
	if err := Generate(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	providers, err := filepath.Glob(filepath.Join(fixture.directory, "configs", "*.json"))
	if err != nil || len(providers) != len(providerMap) {
		t.Fatalf("provider inventory=%v error=%v", providers, err)
	}
	openai := fixture.provider(t, "openai")
	if len(openai.Models) != 3 || openai.Models[0].ID != "free" || openai.Models[1].ID != "model" || openai.Models[2].ID != "unknown" {
		t.Fatalf("filtered and ordered models: %+v", openai.Models)
	}
	free := openai.Models[0]
	if cost, err := free.Pricing.Cost(modelcatalog.Usage{InputTokens: 10}); err != nil || cost != 0 || free.Pricing[0].CacheReadPer1M == nil || *free.Pricing[0].CacheReadPer1M != 0 || free.Pricing[0].CacheWritePer1M != nil {
		t.Fatalf("free pricing presence changed: %+v, cost=%g error=%v", free.Pricing, cost, err)
	}
	if _, err := openai.Models[2].Pricing.Cost(modelcatalog.Usage{}); !errors.Is(err, modelcatalog.ErrUnknownPricing) {
		t.Fatalf("unknown pricing fabricated: %v", err)
	}
	if openai.Models[1].Reasoning.DefaultLevel != "low" {
		t.Fatal("local ladder was not used for its provider")
	}
	if got := fixture.provider(t, "vertexai").Models[0]; got.ID != "model@default" || got.Reasoning.DefaultLevel != "medium" || got.Pricing[0].InputPer1M != 3 {
		t.Fatalf("Vertex borrowed another provider's facts: %+v", got)
	}
	if got := fixture.provider(t, "anthropic").Models[0]; got.Reasoning.DefaultLevel != "high" || got.Pricing[0].InputPer1M != 1 {
		t.Fatalf("Anthropic provider facts changed: %+v", got)
	}
	if got := fixture.provider(t, "together").Models[0].ID; got != "essentialai/rnj-1-instruct" {
		t.Fatalf("official model ID = %q", got)
	}
}

func TestGenerateRejectsConflictsBeforeAnyCatalogWrite(t *testing.T) {
	cases := []struct {
		name   string
		change func(*generatorFixture)
	}{
		{"missing provider", func(f *generatorFixture) { delete(f.api, "openai") }},
		{"identity conflict", func(f *generatorFixture) { f.api["openai"].Models["wrong"] = apiModel{ID: "other"} }},
		{"duplicate normalized model", func(f *generatorFixture) {
			f.api["togetherai"].Models["essentialai/rnj-1-instruct"] = apiModel{ID: "essentialai/rnj-1-instruct"}
		}},
		{"dead official correction", func(f *generatorFixture) { delete(f.api["togetherai"].Models, "essentialai/Rnj-1-Instruct") }},
		{"dead local augmentation", func(f *generatorFixture) { f.augmentations["openai"]["missing"] = augEntry{Levels: []string{"high"}} }},
		{"nonreasoner augmentation", func(f *generatorFixture) {
			m := f.api["openai"].Models["model"]
			m.Reasoning = false
			f.api["openai"].Models["model"] = m
		}},
		{"upstream replaces augmentation", func(f *generatorFixture) {
			for i := range f.ladders {
				if f.ladders[i].ID == "openai" {
					f.ladders[i].Models[0].ReasoningLevels = []string{"high"}
				}
			}
		}},
		{"invalid model", func(f *generatorFixture) {
			f.api["openai"].Models["invalid"] = apiModel{ID: "invalid", Knowledge: "2026-13"}
		}},
		{"duplicate ladder provider", func(f *generatorFixture) { f.ladders = append(f.ladders, f.ladders[0]) }},
		{"duplicate ladder model", func(f *generatorFixture) {
			for i := range f.ladders {
				if f.ladders[i].ID == "anthropic" {
					f.ladders[i].Models = append(f.ladders[i].Models, f.ladders[i].Models[0])
				}
			}
		}},
		{"missing ladder provider", func(f *generatorFixture) { f.ladders = nil }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGeneratorFixture(t)
			test.change(fixture)
			config := fixture.write(t)
			previous := filepath.Join(fixture.directory, "configs", "openai.json")
			if err := os.WriteFile(previous, []byte("previous catalog"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := Generate(t.Context(), config); err == nil {
				t.Fatal("ambiguous generation admitted")
			}
			remaining, err := os.ReadFile(previous)
			if err != nil || string(remaining) != "previous catalog" {
				t.Fatalf("failed validation changed the catalog: %s, %v", remaining, err)
			}
			files, err := filepath.Glob(filepath.Join(fixture.directory, "configs", "*.json"))
			if err != nil || len(files) != 1 {
				t.Fatalf("failed validation wrote providers: %v, %v", files, err)
			}
		})
	}
}

func TestGenerateRetainsSourceAndDestinationFailures(t *testing.T) {
	for _, name := range []string{"missing source", "malformed source", "missing ladders", "malformed ladders", "missing augmentations", "malformed augmentations", "null augmentations", "unknown augmentation field", "write failure"} {
		t.Run(name, func(t *testing.T) {
			fixture := newGeneratorFixture(t)
			config := fixture.write(t)
			augmentationPath := filepath.Join(fixture.directory, "augmentations.json")
			var mutationErr error
			switch name {
			case "missing source":
				mutationErr = os.Remove(config.Source)
			case "malformed source":
				mutationErr = os.WriteFile(config.Source, []byte(`{`), 0o644)
			case "missing ladders":
				mutationErr = os.Remove(config.Ladders)
			case "malformed ladders":
				mutationErr = os.WriteFile(config.Ladders, []byte(`{`), 0o644)
			case "missing augmentations":
				mutationErr = os.Remove(augmentationPath)
			case "malformed augmentations":
				mutationErr = os.WriteFile(augmentationPath, []byte(`{`), 0o644)
			case "null augmentations":
				mutationErr = os.WriteFile(augmentationPath, []byte(`null`), 0o644)
			case "unknown augmentation field":
				mutationErr = os.WriteFile(augmentationPath, []byte(`{"openai":{"model":{"level":"high"}}}`), 0o644)
			case "write failure":
				mutationErr = os.Remove(filepath.Join(fixture.directory, "configs"))
			}
			if mutationErr != nil {
				t.Fatal(mutationErr)
			}
			if err := Generate(t.Context(), config); err == nil {
				t.Fatal("failed boundary was hidden")
			}
		})
	}
	if err := Generate(t.Context(), Config{}); err == nil {
		t.Fatal("missing configuration admitted")
	}
}

func TestNativeModelProjectionKeepsOnlyAdmittedFacts(t *testing.T) {
	model := apiModel{ID: "model", Name: "Named", Knowledge: "2025-01", ReleaseDate: "2026-01-02", LastUpdated: "2026-02-03", Status: statusDeprecated, ToolCall: true, StructuredOutput: true}
	model.Modalities.Input, model.Modalities.Output = []string{"text", "image"}, []string{"text"}
	model.Limit.Context, model.Limit.Input, model.Limit.Output = 100, 80, 20
	projected, err := model.modelInfo(modelcatalog.Reasoning{Levels: []string{"high"}})
	if err != nil || projected.KnowledgeCutoff != time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC) || !projected.Deprecated || !projected.ToolCall || !projected.StructuredOutput || !projected.Reasoning.IsZero() || projected.Limits.MaxInputTokens != 80 || !projected.Modalities.AcceptsInput(modelcatalog.ModalityImage) {
		t.Fatalf("projection=%+v error=%v", projected, err)
	}
	for _, change := range []func(*apiModel){
		func(m *apiModel) { m.Knowledge = "invalid" },
		func(m *apiModel) { m.ReleaseDate = "invalid" },
		func(m *apiModel) { m.LastUpdated = "invalid" },
		func(m *apiModel) { m.Limit.Input = -1 },
		func(m *apiModel) { m.Cost = &apiCost{Input: new(1.0)} },
	} {
		invalid := model
		change(&invalid)
		if _, err := invalid.modelInfo(modelcatalog.Reasoning{}); err == nil {
			t.Fatalf("invalid source model admitted: %+v", invalid)
		}
	}
}

func TestNativePricingAdmitsRequiredRatesAndOrderedContextBands(t *testing.T) {
	for _, raw := range []string{`{}`, `{"input":0}`, `{"output":0}`, `{"input":0,"output":0,"tiers":[{"tier":{"type":"context","size":10},"input":1}]}`, `{"input":0,"output":0,"tiers":[{"tier":{"type":"context","size":0},"input":1,"output":2}]}`} {
		var cost apiCost
		if err := jsonv2.Unmarshal([]byte(raw), &cost); err != nil {
			t.Fatal(err)
		}
		if _, err := cost.pricing(); err == nil {
			t.Errorf("incomplete rates admitted: %s", raw)
		}
	}
	var cost apiCost
	const rates = `{"input":1,"output":2,"cache_read":0,"tiers":[{"tier":{"type":"context","size":20},"input":3,"output":4},{"tier":{"type":"priority","size":10},"input":99,"output":99},{"tier":{"type":"context","size":10},"input":2,"output":3,"cache_write":0}]}`
	if err := jsonv2.Unmarshal([]byte(rates), &cost); err != nil {
		t.Fatal(err)
	}
	bands, err := cost.pricing()
	if err != nil || len(bands) != 3 || bands[1].Threshold != 10 || bands[2].Threshold != 20 || bands[0].CacheReadPer1M == nil || *bands[0].CacheReadPer1M != 0 || bands[1].CacheWritePer1M == nil {
		t.Fatalf("context rates=%+v error=%v", bands, err)
	}
}

type failingReader struct{ failure error }

func (f failingReader) Read([]byte) (int, error) { return 0, f.failure }

func TestSourceReadsAreBoundedAndRetainFailures(t *testing.T) {
	failure := errors.New("source failure")
	if _, err := readSource(failingReader{failure}); !errors.Is(err, failure) {
		t.Fatalf("read error lost: %v", err)
	}
	if _, err := readSource(bytes.NewReader(bytes.Repeat([]byte("x"), int(maximumSourceBytes)+1))); err == nil {
		t.Fatal("unbounded source admitted")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/failed" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, "source")
	}))
	defer server.Close()
	if body, err := fetchSource(t.Context(), server.URL); err != nil || string(body) != "source" {
		t.Fatalf("HTTP source=%q error=%v", body, err)
	}
	if _, err := fetchSource(t.Context(), server.URL+"/failed"); err == nil {
		t.Fatal("failed HTTP status admitted")
	}
	if _, err := fetchSource(t.Context(), "http://bad\x00host"); err == nil {
		t.Fatal("malformed source URL admitted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := fetchSource(ctx, server.URL); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if _, err := fetchSource(t.Context(), filepath.Join(t.TempDir(), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing source lost: %v", err)
	}
}

func TestWriteJSONRejectsUnencodableData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.json")
	if err := writeJSON(path, make(chan int)); err == nil {
		t.Fatal("unsupported data admitted")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("encoding error created output: %v", err)
	}
	if err := writeJSON(path, map[string]string{"b": "two", "a": "one"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.HasSuffix(string(data), "\n") || strings.Index(string(data), `"a"`) > strings.Index(string(data), `"b"`) {
		t.Fatalf("unstable JSON output: %s, %v", data, err)
	}
	if writeErr := writeJSON(path, make(chan int)); writeErr == nil {
		t.Fatal("invalid replacement admitted")
	}
	retained, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(retained, data) {
		t.Fatalf("failed replacement changed prior data: %s, %v", retained, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("published file permissions: %v, %v", info, err)
	}
	blocked := filepath.Join(filepath.Dir(path), "directory.json")
	if mkdirErr := os.Mkdir(blocked, 0o755); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	if writeErr := writeJSON(blocked, "value"); writeErr == nil {
		t.Fatal("failed atomic publication was hidden")
	}
	temporary, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".catalog-*.json"))
	if err != nil || len(temporary) != 0 {
		t.Fatalf("unpublished temporary files leaked: %v, %v", temporary, err)
	}
}
