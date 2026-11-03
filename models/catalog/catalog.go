package catalog

import (
	"embed"
	jsonv2 "encoding/json/v2"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

// Provider is one provider's catalog entry.
type Provider struct {
	ID     string  `json:"id"`
	Models []Model `json:"models"`
}

func (p Provider) Clone() Provider {
	models := make([]Model, len(p.Models))
	for index, model := range p.Models {
		models[index] = model.Clone()
	}
	p.Models = models
	return p
}

type providerConfig struct {
	Provider string  `json:"provider"`
	Models   []Model `json:"models"`
}

//go:embed configs/*.json
var configs embed.FS

type catalogEntry struct {
	provider Provider
	models   map[string]Model
}

var entries = mustLoad()

func mustLoad() map[string]catalogEntry {
	providers, err := loadCatalog(configs)
	if err != nil {
		panic(err)
	}
	return providers
}

// validProviderID admits the lowercase spelling adapters use for Provider, so
// one identity can never be cataloged under two spellings.
func validProviderID(id string) bool {
	return id != "" && !strings.ContainsFunc(id, func(r rune) bool { return (r < 'a' || r > 'z') && (r < '0' || r > '9') })
}

func loadCatalog(source fs.FS) (map[string]catalogEntry, error) {
	files, err := fs.Glob(source, "configs/*.json")
	if err != nil {
		return nil, fmt.Errorf("catalog: glob configs: %w", err)
	}
	providers := make(map[string]catalogEntry, len(files))
	for _, name := range files {
		raw, err := fs.ReadFile(source, name)
		if err != nil {
			return nil, fmt.Errorf("catalog: read %s: %w", name, err)
		}
		var config providerConfig
		if err := jsonv2.Unmarshal(raw, &config, jsonv2.RejectUnknownMembers(true)); err != nil {
			return nil, fmt.Errorf("catalog: invalid config %s: %w", name, err)
		}
		providerID := strings.TrimSuffix(path.Base(name), path.Ext(name))
		if config.Provider != providerID {
			return nil, fmt.Errorf("catalog: provider %q differs from file identity %q", config.Provider, providerID)
		}
		if !validProviderID(config.Provider) {
			return nil, fmt.Errorf("catalog: provider %q is not a lowercase adapter identity", config.Provider)
		}
		models := make(map[string]Model, len(config.Models))
		for _, model := range config.Models {
			if err := model.Validate(); err != nil {
				return nil, fmt.Errorf("catalog: %s model %q: %w", name, model.ID, err)
			}
			if _, exists := models[model.ID]; exists {
				return nil, fmt.Errorf("catalog: duplicate model %q in %s", model.ID, name)
			}
			models[model.ID] = model
		}
		if _, exists := providers[config.Provider]; exists {
			return nil, fmt.Errorf("catalog: duplicate provider %q", config.Provider)
		}
		providers[config.Provider] = catalogEntry{
			provider: Provider{ID: config.Provider, Models: config.Models},
			models:   models,
		}
	}
	return providers, nil
}

// LookupModel returns one model for a provider/model pair. Provider is an
// adapter's Provider constant. The returned value owns its slices.
func LookupModel(provider, modelID string) (Model, bool) {
	entry, ok := entries[provider]
	if !ok {
		return Model{}, false
	}
	model, ok := entry.models[modelID]
	if !ok {
		return Model{}, false
	}
	return model.Clone(), true
}

// Models returns every cataloged model for provider, or nil when unknown.
// Order is unspecified and returned values own their slices.
func Models(provider string) []Model {
	entry, ok := entries[provider]
	if !ok {
		return nil
	}
	return entry.provider.Clone().Models
}

// LookupProvider returns one provider's complete catalog entry.
func LookupProvider(provider string) (Provider, bool) {
	entry, ok := entries[provider]
	if !ok {
		return Provider{}, false
	}
	return entry.provider.Clone(), true
}
