// Package generator keeps upstream model facts and reasoning ladders separate
// so a provider never inherits prices or defaults from another endpoint.
package generator

import (
	"cmp"
	"context"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
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
	DefaultSource       = "https://models.dev/api.json"
	DefaultLadderSource = "https://catwalk.charm.sh/v2/providers"
	maximumSourceBytes  = int64(32 * 1024 * 1024)
	sourceTimeout       = 30 * time.Second
	statusDeprecated    = "deprecated"
	tierContext         = "context"
	providerVertexAI    = "vertexai"
	monthDateLayout     = "2006-01"
	generatedFileMode   = 0o644
)

// providerMap maps a models.dev provider id to our config/provider name
// (the adapter's Provider const, lowercased — Lookup matches case-folded).
// OpenAI-compat and managed delegators keep their own name: vertexai is
// sourced from google-vertex, zhipu from zhipuai. Perplexity's models.dev entry
// describes the retired Sonar API, not the current Agent API; do not import its
// identities or copy other providers' prices and capabilities into that binding.
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
	Cost  *apiCost `json:"cost"`
	Limit struct {
		Context int64 `json:"context"`
		Input   int64 `json:"input"`
		Output  int64 `json:"output"`
	} `json:"limit"`
}

func (a apiModel) isChat() bool {
	lid, fam := strings.ToLower(a.ID), strings.ToLower(a.Family)
	if strings.Contains(fam, "embed") || strings.Contains(lid, "embed") || strings.Contains(lid, "rerank") {
		return false
	}
	if len(a.Modalities.Output) > 0 && !slices.Contains(a.Modalities.Output, string(modelcatalog.ModalityText)) {
		return false
	}
	return true
}

func (a apiModel) modelInfo(ladder modelcatalog.Reasoning) (modelcatalog.Model, error) {
	knowledge, err := parseDate(a.Knowledge)
	if err != nil {
		return modelcatalog.Model{}, fmt.Errorf("knowledge cutoff: %w", err)
	}
	released, err := parseDate(a.ReleaseDate)
	if err != nil {
		return modelcatalog.Model{}, fmt.Errorf("release date: %w", err)
	}
	updated, err := parseDate(a.LastUpdated)
	if err != nil {
		return modelcatalog.Model{}, fmt.Errorf("last update: %w", err)
	}
	pricing, err := a.Cost.pricing()
	if err != nil {
		return modelcatalog.Model{}, err
	}
	info := modelcatalog.Model{
		ID:               a.ID,
		DisplayName:      a.Name,
		KnowledgeCutoff:  knowledge,
		ReleaseDate:      released,
		LastUpdated:      updated,
		Deprecated:       a.Status == statusDeprecated,
		ToolCall:         a.ToolCall,
		StructuredOutput: a.StructuredOutput,
		Pricing:          pricing,
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
	if err := info.Validate(); err != nil {
		return modelcatalog.Model{}, err
	}
	return info, nil
}

type apiCost struct {
	Input      *float64  `json:"input"`
	Output     *float64  `json:"output"`
	CacheRead  *float64  `json:"cache_read"`
	CacheWrite *float64  `json:"cache_write"`
	Tiers      []apiTier `json:"tiers"`
}

// Unknown rates must stay absent; zero-priced tokens are a valid commercial
// fact and cannot serve as an absence marker.
func (a *apiCost) pricing() (modelcatalog.PricingSchedule, error) {
	if a == nil {
		return nil, nil
	}
	if a.Input == nil || a.Output == nil {
		return nil, errors.New("model cost requires input and output rates")
	}
	bands := modelcatalog.PricingSchedule{{
		InputPer1M:      *a.Input,
		OutputPer1M:     *a.Output,
		CacheReadPer1M:  a.CacheRead,
		CacheWritePer1M: a.CacheWrite,
	}}
	for _, t := range a.Tiers {
		if t.Tier.Type != tierContext {
			continue
		}
		if t.Input == nil || t.Output == nil || t.Tier.Size <= 0 {
			return nil, errors.New("context cost tier requires a positive threshold and both token rates")
		}
		bands = append(bands, modelcatalog.Pricing{
			Threshold:       t.Tier.Size,
			InputPer1M:      *t.Input,
			OutputPer1M:     *t.Output,
			CacheReadPer1M:  t.CacheRead,
			CacheWritePer1M: t.CacheWrite,
		})
	}
	slices.SortFunc(bands, func(a, b modelcatalog.Pricing) int {
		return cmp.Compare(a.Threshold, b.Threshold)
	})
	return bands, nil
}

// apiTier is one models.dev tiered-pricing step: rates that take over
// once the prompt exceeds tier.size tokens (tier.type == "context").
type apiTier struct {
	Input      *float64 `json:"input"`
	Output     *float64 `json:"output"`
	CacheRead  *float64 `json:"cache_read"`
	CacheWrite *float64 `json:"cache_write"`
	Tier       struct {
		Type string `json:"type"`
		Size int64  `json:"size"`
	} `json:"tier"`
}

type apiProvider struct {
	Models map[string]apiModel `json:"models"`
}

type augEntry struct {
	Levels       []string `json:"levels"`
	DefaultLevel string   `json:"default_level"`
}

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
	if provider != providerVertexAI {
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
// once catwalk starts publishing one for that model — keeping the local copy
// then leaves two sources able to advance the same fact. Either way the entry
// has become indistinguishable from a typo, and invisible unless the generator
// fails on it.
type overlay[T any] struct {
	label   string
	entries map[string]map[string]T
	used    map[string]bool
}

func newOverlay[T any](label string, entries map[string]map[string]T) *overlay[T] {
	return &overlay[T]{label: label, entries: entries, used: make(map[string]bool)}
}

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

type providerConfig struct {
	Provider string               `json:"provider"`
	Models   []modelcatalog.Model `json:"models"`
}

type Config struct {
	Source    string
	Ladders   string
	Directory string
}

func (c Config) validate() error {
	if c.Source == "" || c.Ladders == "" || c.Directory == "" {
		return errors.New("catalog generator: source, ladders, and directory are required")
	}
	return nil
}

func Generate(ctx context.Context, config Config) error {
	if err := config.validate(); err != nil {
		return err
	}
	api, err := loadAPI(ctx, config.Source)
	if err != nil {
		return fmt.Errorf("load %s: %w", config.Source, err)
	}
	ladders, err := loadEffortLadders(ctx, config.Ladders)
	if err != nil {
		return fmt.Errorf("load %s: %w", config.Ladders, err)
	}
	augs, err := loadAugmentations(filepath.Join(config.Directory, "augmentations.json"))
	if err != nil {
		return fmt.Errorf("load augmentations: %w", err)
	}
	officialIDs := newOverlay("officialModelIDs", officialModelIDs)

	// Validate every provider before writing, so bad upstream data cannot publish
	// a partial catalog or silently replace an existing model with a duplicate.
	generated := make(map[string][]modelcatalog.Model, len(providerMap))
	for apiID, provider := range providerMap {
		sourceProvider, found := api[apiID]
		if !found {
			return fmt.Errorf("provider %q not in source", apiID)
		}
		var models []modelcatalog.Model
		seen := make(map[string]struct{}, len(sourceProvider.Models))
		for id, sourceModel := range sourceProvider.Models {
			if !sourceModel.isChat() {
				continue
			}
			if sourceModel.ID != id {
				return fmt.Errorf("provider %s: model key %q differs from id %q", provider, id, sourceModel.ID)
			}
			if officialID := officialIDs.take(provider, id); officialID != "" {
				sourceModel.ID = officialID
			}
			if _, duplicate := seen[sourceModel.ID]; duplicate {
				return fmt.Errorf("provider %s: duplicate model %q", provider, sourceModel.ID)
			}
			seen[sourceModel.ID] = struct{}{}
			var ladder modelcatalog.Reasoning
			if sourceModel.Reasoning {
				var found bool
				ladder, found = ladders.lookup(provider, id)
				if !found {
					aug := augs.take(provider, id)
					ladder = modelcatalog.Reasoning{Levels: aug.Levels, DefaultLevel: aug.DefaultLevel}
				}
			}
			model, err := sourceModel.modelInfo(ladder)
			if err != nil {
				return fmt.Errorf("provider %s model %s: %w", provider, id, err)
			}
			models = append(models, model)
		}
		slices.SortFunc(models, func(a, b modelcatalog.Model) int { return cmp.Compare(a.ID, b.ID) })
		generated[provider] = models
	}
	if dead := deadOverlayEntries(augs, officialIDs); len(dead) > 0 {
		return fmt.Errorf("overlay entries reached no generated model: %s", strings.Join(dead, ", "))
	}
	for provider, models := range generated {
		output := filepath.Join(config.Directory, "configs", provider+".json")
		if err := writeJSON(output, providerConfig{Provider: provider, Models: models}); err != nil {
			return fmt.Errorf("write %s: %w", output, err)
		}
	}
	return nil
}

func parseDate(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.DateOnly, monthDateLayout} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid model date %q", value)
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

func fetchSource(ctx context.Context, source string) ([]byte, error) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		deadline, cancel := context.WithTimeout(ctx, sourceTimeout)
		defer cancel()
		request, err := http.NewRequestWithContext(deadline, http.MethodGet, source, nil)
		if err != nil {
			return nil, err
		}
		resp, err := http.DefaultClient.Do(request)
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

func loadAPI(ctx context.Context, source string) (map[string]apiProvider, error) {
	raw, err := fetchSource(ctx, source)
	if err != nil {
		return nil, err
	}
	var api map[string]apiProvider
	if err := jsonv2.Unmarshal(raw, &api); err != nil {
		return nil, err
	}
	return api, nil
}

func loadEffortLadders(ctx context.Context, source string) (*effortLadders, error) {
	raw, err := fetchSource(ctx, source)
	if err != nil {
		return nil, err
	}
	var providers []catwalkProvider
	if err := jsonv2.Unmarshal(raw, &providers); err != nil {
		return nil, err
	}
	byCatwalkID := make(map[string]catwalkProvider, len(providers))
	for _, provider := range providers {
		if _, exists := byCatwalkID[provider.ID]; exists {
			return nil, fmt.Errorf("duplicate catwalk provider %q", provider.ID)
		}
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
			if _, exists := byModel[model.ID]; exists {
				return nil, fmt.Errorf("catwalk provider %s: duplicate model %q", catwalkID, model.ID)
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
	if err := jsonv2.Unmarshal(raw, &entries, jsonv2.RejectUnknownMembers(true)); err != nil {
		return nil, err
	}
	if entries == nil {
		return nil, errors.New("augmentations must be an object")
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
	// A failed write must not truncate the previously published provider data.
	file, err := os.CreateTemp(filepath.Dir(path), ".catalog-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(append(b, '\n'))
	if writeErr == nil {
		writeErr = file.Chmod(generatedFileMode)
	}
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
