//go:build ignore

// Command gen regenerates the embedded model catalog (configs/*.json) from two
// community model databases.
//
// To refresh the catalog, run the wrapper from the repository root — it
// regenerates, prints what moved, and runs this module's checks:
//
//	scripts/update-model-catalog.sh
//
// This command is what the wrapper drives, and takes the same flags. Run it
// from this directory:
//
//	go run gen.go                          # fetch both sources live
//	go run gen.go -source ./api.json       # or read local snapshots
//	go run gen.go -ladders ./catwalk.json
//
// Each source owns a disjoint set of facts, so no field has two origins that
// could disagree:
//
//   - models.dev decides which models exist and carries everything about
//     them except the reasoning effort ladder, of which it knows only a bool.
//   - catwalk (the database behind Crush) carries the ladder, which is where
//     this catalog's ladders were originally backfilled from.
//
// Pipeline:
//
//  1. Load models.dev's api.json — a fully-resolved JSON map of
//     provider id -> { models: { model id -> spec } }. It's already
//     resolved (TOML parsed, [extends] applied), so this tool needs only
//     encoding/json/v2 and no TOML dependency.
//  2. Keep only the providers are available with a chat adapter for (providerMap),
//     and only chat models (drop embedding / TTS / image-generation —
//     output modality not "text", or an embedding family).
//  3. Map each spec into a catalog.Model. The output is marshaled from
//     the real struct, so the generated JSON can never drift from the Go
//     type — that's the reason this is a Go program and not a script.
//  4. Attach the effort ladder catwalk publishes for that provider's model.
//     augmentations.json fills a ladder catwalk does not have, mirroring
//     LangChain's profile_augmentations approach; an entry that catwalk
//     later covers fails the run rather than persisting as a second copy.
//
// To add a provider: add it to providerMap (left = models.dev provider id,
// right = the adapter's Provider const, lowercased), add it to
// catwalkProviderMap if catwalk serves the same endpoint, and re-run.
//
// To fill a ladder neither source publishes: add it to augmentations.json under
// our provider name and the models.dev model id. Never copy one from a sibling
// provider — the same model can take a different default through a different
// endpoint, so a copied ladder publishes a default no endpoint uses. Once
// catwalk starts publishing that ladder, the run fails until the local entry is
// removed, which keeps the ladder single-sourced.
package main

import (
	"cmp"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	modelcatalog "github.com/Tangerg/scope/models/catalog"
)

const (
	defaultSource       = "https://models.dev/api.json"
	defaultLadderSource = "https://catwalk.charm.sh/v2/providers"
	maximumSourceBytes  = int64(32 * 1024 * 1024)
)

// providerMap maps a models.dev provider id to our config/provider name
// (the adapter's Provider const, lowercased — Lookup matches case-folded).
// OpenAI-compat and managed delegators keep their own name: vertexai is
// sourced from google-vertex, zhipu from zhipuai.
var providerMap = map[string]string{
	"anthropic":      "anthropic",
	"openai":         "openai",
	"google":         "google",
	"google-vertex":  "vertexai",
	"deepseek":       "deepseek",
	"groq":           "groq",
	"xai":            "xai",
	"minimax":        "minimax",
	"zhipuai":        "zhipu",
	"alibaba":        "alibaba",
	"azure":          "azureopenai",
	"amazon-bedrock": "amazonbedrock",
	"fireworks-ai":   "fireworks",
	"huggingface":    "huggingface",
	"mistral":        "mistral",
	"moonshotai":     "moonshot",
	"ollama-cloud":   "ollama",
	"openrouter":     "openrouter",
	"perplexity":     "perplexity",
	"togetherai":     "together",
	"xiaomi":         "xiaomi",
}

// catwalkProviderMap maps our provider name to the catwalk provider serving
// the same endpoint. A provider absent here has no catwalk counterpart, so its
// models carry no effort ladder unless augmentations.json supplies one.
//
// The mapping is one-to-one on purpose. catwalk also publishes regional and
// plan-specific twins (bedrock-europe, alibaba-us, zhipu-coding, minimax-china)
// whose model ids overlap their primary, and merging them would need a rule for
// which twin wins — a rule that only exists because two sources could advance
// the same fact. The twins add a handful of models; the ambiguity is not worth
// them.
var catwalkProviderMap = map[string]string{
	"anthropic":     "anthropic",
	"openai":        "openai",
	"google":        "gemini",
	"vertexai":      "vertexai",
	"deepseek":      "deepseek",
	"groq":          "groq",
	"xai":           "xai",
	"zhipu":         "zhipu",
	"minimax":       "minimax",
	"moonshot":      "moonshot",
	"azureopenai":   "azure",
	"amazonbedrock": "bedrock",
	"fireworks":     "fireworks",
	"huggingface":   "huggingface",
	"openrouter":    "openrouter",
	"alibaba":       "alibaba-singapore",
}

// officialModelIDs corrects an upstream id that does not match the one the
// provider's own API accepts.
var officialModelIDs = map[string]map[string]string{
	"together": {
		"essentialai/Rnj-1-Instruct": "essentialai/rnj-1-instruct",
	},
}

// apiModel mirrors the subset of a models.dev model spec consumed here.
type apiModel struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Family           string `json:"family"`
	Reasoning        bool   `json:"reasoning"`
	ToolCall         bool   `json:"tool_call"`
	StructuredOutput bool   `json:"structured_output"`
	Knowledge        string `json:"knowledge"`
	ReleaseDate      string `json:"release_date"`
	LastUpdated      string `json:"last_updated"`
	Status           string `json:"status"`
	Modalities       struct {
		Input  []string `json:"input"`
		Output []string `json:"output"`
	} `json:"modalities"`
	Cost  apiCost `json:"cost"`
	Limit struct {
		Context int64 `json:"context"`
		Input   int64 `json:"input"`
		Output  int64 `json:"output"`
	} `json:"limit"`
}

// isChat keeps only chat models — embeddings (by family/id) and models
// that don't emit text (TTS, image generation) are dropped.
func (a apiModel) isChat() bool {
	lid, fam := strings.ToLower(a.ID), strings.ToLower(a.Family)
	if strings.Contains(fam, "embed") || strings.Contains(lid, "embed") || strings.Contains(lid, "rerank") {
		return false
	}
	if len(a.Modalities.Output) > 0 && !slices.Contains(a.Modalities.Output, "text") {
		return false
	}
	return true
}

func (a apiModel) modelInfo(ladder modelcatalog.Reasoning) modelcatalog.Model {
	info := modelcatalog.Model{
		ID:               a.ID,
		DisplayName:      a.Name,
		KnowledgeCutoff:  parseDate(a.Knowledge),
		ReleaseDate:      parseDate(a.ReleaseDate),
		LastUpdated:      parseDate(a.LastUpdated),
		Deprecated:       a.Status == "deprecated",
		ToolCall:         a.ToolCall,
		StructuredOutput: a.StructuredOutput,
		Pricing:          a.Cost.pricing(),
		Modalities: modelcatalog.Modalities{
			Input:  toModalities(a.Modalities.Input),
			Output: toModalities(a.Modalities.Output),
		},
		Limits: modelcatalog.Limits{
			ContextWindow:   a.Limit.Context,
			MaxInputTokens:  a.Limit.Input,
			MaxOutputTokens: a.Limit.Output,
		},
	}
	// models.dev owns whether a model reasons at all; the ladder is only
	// meaningful for one that does.
	if a.Reasoning {
		ladder.Supported = true
		info.Reasoning = ladder
	}
	return info
}

// apiCost mirrors a models.dev [cost] block: base rates plus optional
// context tiers.
type apiCost struct {
	Input      float64   `json:"input"`
	Output     float64   `json:"output"`
	CacheRead  float64   `json:"cache_read"`
	CacheWrite float64   `json:"cache_write"`
	Tiers      []apiTier `json:"tiers"`
}

// pricing maps a models.dev [cost] block to a banded rate card: the
// base band (threshold 0) plus a band per context tier, sorted ascending
// by threshold ([modelcatalog.PricingSchedule.Cost] scans back to front). Non-context tiers are
// skipped — only prompt-size repricing is modeled. Returns nil when there's
// no input rate (unknown pricing).
func (a apiCost) pricing() modelcatalog.PricingSchedule {
	if a.Input == 0 && a.Output == 0 {
		return nil
	}
	bands := modelcatalog.PricingSchedule{{
		InputPer1M:      a.Input,
		OutputPer1M:     a.Output,
		CacheReadPer1M:  a.CacheRead,
		CacheWritePer1M: a.CacheWrite,
	}}
	for _, t := range a.Tiers {
		if t.Tier.Type != "context" || t.Tier.Size == 0 {
			continue
		}
		bands = append(bands, modelcatalog.Pricing{
			Threshold:       t.Tier.Size,
			InputPer1M:      t.Input,
			OutputPer1M:     t.Output,
			CacheReadPer1M:  t.CacheRead,
			CacheWritePer1M: t.CacheWrite,
		})
	}
	slices.SortFunc(bands, func(a, b modelcatalog.Pricing) int {
		return cmp.Compare(a.Threshold, b.Threshold)
	})
	return bands
}

// apiTier is one models.dev tiered-pricing step: rates that take over
// once the prompt exceeds tier.size tokens (tier.type == "context").
type apiTier struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
	Tier       struct {
		Type string `json:"type"`
		Size int64  `json:"size"`
	} `json:"tier"`
}

type apiProvider struct {
	Models map[string]apiModel `json:"models"`
}

// augEntry is a local augmentation overlay — fields models.dev lacks.
type augEntry struct {
	Levels       []string `json:"levels"`
	DefaultLevel string   `json:"default_level"`
}

// catwalkProvider mirrors the subset of a catwalk provider consumed here.
type catwalkProvider struct {
	ID     string         `json:"id"`
	Models []catwalkModel `json:"models"`
}

type catwalkModel struct {
	ID              string   `json:"id"`
	ReasoningLevels []string `json:"reasoning_levels"`
	DefaultEffort   string   `json:"default_reasoning_effort"`
}

// effortLadders owns the reasoning effort ladder, keyed by our provider name
// then upstream model id.
//
// models.dev knows only whether a model reasons, so the ladder comes from
// catwalk — the database behind Crush, and the source this catalog's ladders
// were originally backfilled from.
//
// A ladder is read within one provider and never borrowed across providers.
// The same model reached through two endpoints can take a different default:
// claude-opus-4-6 defaults to high direct from Anthropic and to medium through
// Vertex, so a cross-provider fallback would publish a number no endpoint
// actually uses.
type effortLadders struct {
	byProvider map[string]map[string]modelcatalog.Reasoning
}

func (e *effortLadders) lookup(provider, id string) (modelcatalog.Reasoning, bool) {
	reasoning, ok := e.byProvider[provider][catwalkModelID(provider, id)]
	return reasoning, ok
}

// catwalkModelID reconciles the one id spelling the two sources disagree on:
// models.dev qualifies a Vertex model with the version it is pinned to
// (claude-opus-4-6@default), catwalk names the model alone. The qualifier
// selects a deployment, not a different ladder.
func catwalkModelID(provider, id string) string {
	if provider != "vertexai" {
		return id
	}
	name, _, _ := strings.Cut(id, "@")
	return name
}

// overlay is a hand-maintained correction to the source, keyed by our provider
// name then upstream model id.
//
// Every entry has to reach a generated model. An entry goes unused when
// upstream retires or renames its model, and an effort ladder also goes unused
// once catwalk starts publishing one for that model — at which point keeping
// the local copy would leave two sources able to advance the same fact. Either
// way the entry is now indistinguishable from a typo, and invisible unless the
// generator says so, so take records what it hands out and deadEntries reports
// the rest, which main turns into a failure.
type overlay[T any] struct {
	label   string
	entries map[string]map[string]T
	used    map[string]bool
}

func newOverlay[T any](label string, entries map[string]map[string]T) *overlay[T] {
	return &overlay[T]{label: label, entries: entries, used: make(map[string]bool)}
}

// take returns the zero value when nothing is overlaid, so a caller reads a
// missing entry as "no correction" without a second branch.
func (o *overlay[T]) take(provider, id string) T {
	entry, ok := o.entries[provider][id]
	if !ok {
		var zero T
		return zero
	}
	o.used[provider+"/"+id] = true
	return entry
}

func (o *overlay[T]) deadEntries() []string {
	var dead []string
	for provider, byModel := range o.entries {
		for id := range byModel {
			// Reporting only: model ids contain slashes, so this key is
			// never parsed back apart.
			if key := provider + "/" + id; !o.used[key] {
				dead = append(dead, o.label+" "+key)
			}
		}
	}
	slices.Sort(dead)
	return dead
}

// overlaySource is what main needs from an overlay once every provider is
// mapped, independent of what the overlay carries.
type overlaySource interface {
	deadEntries() []string
}

func deadOverlayEntries(sources ...overlaySource) []string {
	var dead []string
	for _, source := range sources {
		dead = append(dead, source.deadEntries()...)
	}
	return dead
}

// config is the on-disk shape of each configs/<provider>.json.
type config struct {
	Provider string               `json:"provider"`
	Models   []modelcatalog.Model `json:"models"`
}

func main() {
	source := flag.String("source", defaultSource, "models.dev api.json URL or local file path")
	ladderSource := flag.String("ladders", defaultLadderSource, "catwalk providers URL or local file path")
	flag.Parse()

	api, err := loadAPI(*source)
	if err != nil {
		fail("load %s: %v", *source, err)
	}
	ladders, err := loadEffortLadders(*ladderSource)
	if err != nil {
		fail("load %s: %v", *ladderSource, err)
	}
	augs, err := loadAugmentations("augmentations.json")
	if err != nil {
		fail("load augmentations: %v", err)
	}
	officialIDs := newOverlay("officialModelIDs", officialModelIDs)

	// Nothing is written until every provider has been mapped, so a dead
	// overlay entry leaves the checked-in configs untouched rather than
	// half-regenerated.
	generated := make(map[string][]modelcatalog.Model, len(providerMap))
	for apiID, provider := range providerMap {
		p, ok := api[apiID]
		if !ok {
			fail("provider %q not in source", apiID)
		}
		var models []modelcatalog.Model
		for id, m := range p.Models {
			if !m.isChat() {
				continue
			}
			if officialID := officialIDs.take(provider, m.ID); officialID != "" {
				m.ID = officialID
			}
			ladder, ok := ladders.lookup(provider, id)
			if !ok {
				aug := augs.take(provider, id)
				ladder = modelcatalog.Reasoning{Levels: aug.Levels, DefaultLevel: aug.DefaultLevel}
			}
			models = append(models, m.modelInfo(ladder))
		}
		slices.SortFunc(models, func(a, b modelcatalog.Model) int {
			return cmp.Compare(a.ID, b.ID)
		})
		generated[provider] = models
	}
	if dead := deadOverlayEntries(augs, officialIDs); len(dead) > 0 {
		fail("overlay entries reached no generated model: %s", strings.Join(dead, ", "))
	}

	for provider, models := range generated {
		out := filepath.Join("configs", provider+".json")
		if err := writeJSON(out, config{Provider: provider, Models: models}); err != nil {
			fail("write %s: %v", out, err)
		}
		fmt.Printf("%s: %d chat models\n", out, len(models))
	}
}

// parseDate parses a models.dev date — full "2006-01-02" or month-only
// "2006-01" (which lands on the first of the month) — to a time.Time, or
// the zero time when empty / unparseable.
func parseDate(s string) time.Time {
	for _, layout := range []string{"2006-01-02", "2006-01"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func toModalities(in []string) []modelcatalog.Modality {
	if len(in) == 0 {
		return nil
	}
	out := make([]modelcatalog.Modality, len(in))
	for i, s := range in {
		out[i] = modelcatalog.Modality(s)
	}
	return out
}

func fetchSource(source string) ([]byte, error) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		resp, err := http.Get(source)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("fetch %s: HTTP status %s", source, resp.Status)
		}
		return readSource(resp.Body)
	}
	file, err := os.Open(source)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readSource(file)
}

func loadAPI(source string) (map[string]apiProvider, error) {
	raw, err := fetchSource(source)
	if err != nil {
		return nil, err
	}
	var api map[string]apiProvider
	if err := jsonv2.Unmarshal(raw, &api); err != nil {
		return nil, err
	}
	return api, nil
}

func loadEffortLadders(source string) (*effortLadders, error) {
	raw, err := fetchSource(source)
	if err != nil {
		return nil, err
	}
	var providers []catwalkProvider
	if err := jsonv2.Unmarshal(raw, &providers); err != nil {
		return nil, err
	}
	byCatwalkID := make(map[string]catwalkProvider, len(providers))
	for _, provider := range providers {
		byCatwalkID[provider.ID] = provider
	}

	ladders := &effortLadders{byProvider: make(map[string]map[string]modelcatalog.Reasoning, len(catwalkProviderMap))}
	for provider, catwalkID := range catwalkProviderMap {
		source, ok := byCatwalkID[catwalkID]
		if !ok {
			return nil, fmt.Errorf("catwalk provider %q not in source", catwalkID)
		}
		byModel := make(map[string]modelcatalog.Reasoning)
		for _, model := range source.Models {
			if len(model.ReasoningLevels) == 0 {
				continue
			}
			byModel[model.ID] = modelcatalog.Reasoning{
				Supported:    true,
				Levels:       model.ReasoningLevels,
				DefaultLevel: model.DefaultEffort,
			}
		}
		ladders.byProvider[provider] = byModel
	}
	return ladders, nil
}

func loadAugmentations(path string) (*overlay[augEntry], error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := readSource(file)
	if err != nil {
		return nil, err
	}
	var entries map[string]map[string]augEntry
	if err := jsonv2.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	return newOverlay(path, entries), nil
}

func readSource(reader io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, maximumSourceBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maximumSourceBytes {
		return nil, fmt.Errorf("catalog source exceeds %d-byte limit", maximumSourceBytes)
	}
	return raw, nil
}

func writeJSON(path string, v any) error {
	b, err := jsonv2.Marshal(v, jsonv2.Deterministic(true), jsontext.WithIndent("  "))
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "gen: "+format+"\n", args...)
	os.Exit(1)
}
