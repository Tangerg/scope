//go:build ignore

// Command gen regenerates the embedded model catalog (configs/*.json)
// from models.dev — a community model database (the same data behind
// model profiles). Run it from this directory:
//
//	go run gen.go                          # fetch live https://models.dev/api.json
//	go run gen.go -source ./api.json       # or read a local snapshot
//
// Pipeline:
//
//  1. Load models.dev's api.json — a fully-resolved JSON map of
//     provider id -> { models: { model id -> spec } }. It's already
//     resolved (TOML parsed, [extends] applied), so this tool needs only
//     encoding/json and no TOML dependency.
//  2. Keep only the providers are available with a chat adapter for (providerMap),
//     and only chat models (drop embedding / TTS / image-generation —
//     output modality not "text", or an embedding family).
//  3. Map each spec into a catalog.Model. The output is marshaled from
//     the real struct, so the generated JSON can never drift from the Go
//     type — that's the reason this is a Go program and not a script.
//  4. Overlay augmentations.json for fields models.dev lacks. Today that's
//     only reasoning effort levels (models.dev has a bare reasoning bool);
//     the file is the seam to hand-fill anything upstream is missing,
//     mirroring profile_augmentations.
//
// To add a provider: add it to providerMap (left = models.dev provider id,
// right = the adapter's Provider const, lowercased) and re-run.
package main

import (
	"cmp"
	"encoding/json"
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
	defaultSource      = "https://models.dev/api.json"
	maximumSourceBytes = int64(32 * 1024 * 1024)
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

var officialModelIDs = map[string]map[string]string{
	"together": {
		"essentialai/Rnj-1-Instruct": "essentialai/rnj-1-instruct",
	},
}

// officialDeprecatedModelIDs corrects stale upstream status when a provider
// has published a retirement date. Deprecated rows remain available only for
// historical cost attribution; provider adapters do not expose legacy aliases.
var officialDeprecatedModelIDs = map[string]map[string]bool{
	"deepseek": {
		"deepseek-chat":     true,
		"deepseek-reasoner": true,
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

func (a apiModel) modelInfo(aug augEntry) modelcatalog.Model {
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
	if a.Reasoning {
		// models.dev only knows whether a model reasons; effort levels
		// come from the augmentation file.
		info.Reasoning = modelcatalog.Reasoning{
			Supported:    true,
			Levels:       aug.Levels,
			DefaultLevel: aug.DefaultLevel,
		}
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

// config is the on-disk shape of each configs/<provider>.json.
type config struct {
	Provider string               `json:"provider"`
	Models   []modelcatalog.Model `json:"models"`
}

func main() {
	source := flag.String("source", defaultSource, "models.dev api.json URL or local file path")
	flag.Parse()

	api, err := loadAPI(*source)
	if err != nil {
		fail("load %s: %v", *source, err)
	}
	augs, err := loadAugmentations("augmentations.json")
	if err != nil {
		fail("load augmentations: %v", err)
	}

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
			if officialID := officialModelIDs[provider][m.ID]; officialID != "" {
				m.ID = officialID
			}
			if officialDeprecatedModelIDs[provider][m.ID] {
				m.Status = "deprecated"
			}
			models = append(models, m.modelInfo(augs[provider][id]))
		}
		slices.SortFunc(models, func(a, b modelcatalog.Model) int {
			return cmp.Compare(a.ID, b.ID)
		})

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

func loadAPI(source string) (map[string]apiProvider, error) {
	var raw []byte
	var err error
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		resp, e := http.Get(source)
		if e != nil {
			return nil, e
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("catalog.fetchSource: HTTP status %s", resp.Status)
		}
		raw, err = readSource(resp.Body)
	} else {
		var file *os.File
		file, err = os.Open(source)
		if err == nil {
			defer file.Close()
			raw, err = readSource(file)
		}
	}
	if err != nil {
		return nil, err
	}
	var api map[string]apiProvider
	if err := json.Unmarshal(raw, &api); err != nil {
		return nil, err
	}
	return api, nil
}

func loadAugmentations(path string) (map[string]map[string]augEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := readSource(file)
	if err != nil {
		return nil, err
	}
	var augs map[string]map[string]augEntry
	if err := json.Unmarshal(raw, &augs); err != nil {
		return nil, err
	}
	return augs, nil
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
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "gen: "+format+"\n", args...)
	os.Exit(1)
}
