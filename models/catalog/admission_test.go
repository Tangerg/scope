package catalog

import (
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"
)

func TestModelOwnsCatalogFactValidation(t *testing.T) {
	for _, model := range []Model{
		{},
		{ID: "model", Pricing: PricingSchedule{{InputPer1M: -1}}},
		{ID: "model", Reasoning: Reasoning{Levels: []string{"high"}}},
		{ID: "model", Reasoning: Reasoning{Supported: true, Levels: []string{"high", "high"}}},
		{ID: "model", Reasoning: Reasoning{Supported: true, Levels: []string{""}}},
		{ID: "model", Reasoning: Reasoning{Supported: true, Levels: []string{"high"}, DefaultLevel: "low"}},
		{ID: "model", Modalities: Modalities{Input: []Modality{"unknown"}}},
		{ID: "model", Modalities: Modalities{Output: []Modality{ModalityText, ModalityText}}},
		{ID: "model", Limits: Limits{ContextWindow: -1}},
		{ID: "model", Limits: Limits{MaxInputTokens: -1}},
		{ID: "model", Limits: Limits{MaxOutputTokens: -1}},
	} {
		if err := model.Validate(); err == nil {
			t.Errorf("invalid model admitted: %+v", model)
		}
	}
	for _, model := range []Model{
		{ID: "model"},
		{ID: "free", Pricing: PricingSchedule{{}}},
		{ID: "thinking", Reasoning: Reasoning{Supported: true}},
		{ID: "levels", Reasoning: Reasoning{Supported: true, Levels: []string{"low", "high"}}},
		{ID: "media", Modalities: Modalities{Input: []Modality{ModalityText, ModalityImage, ModalityAudio, ModalityVideo, ModalityPDF}, Output: []Modality{ModalityText}}},
	} {
		if err := model.Validate(); err != nil {
			t.Errorf("valid model rejected: %+v: %v", model, err)
		}
	}
}

func TestCatalogRejectsAmbiguousOrMalformedEmbeddedFacts(t *testing.T) {
	for _, body := range []string{
		`null`, `{"provider":"a","models":[{"id":"model","extra":true}]}`,
		`{"provider":"b","models":[{"id":"model"}]}`,
		`{"provider":"a","models":[{"id":""}]}`,
		`{"provider":"a","models":[{"id":"same"},{"id":"same"}]}`,
		`{"provider":"a","models":[{"id":"model","pricing":[{}]}]}`,
	} {
		result, err := loadCatalog(fstest.MapFS{"configs/a.json": {Data: []byte(body)}})
		if err == nil || result != nil {
			t.Errorf("invalid catalog admitted: %s: %v", body, err)
		}
	}
	result, err := loadCatalog(fstest.MapFS{
		"configs/a.json": {Data: []byte(`{"provider":"a","models":[{"id":"one"}]}`)},
		"configs/A.json": {Data: []byte(`{"provider":"A","models":[{"id":"two"}]}`)},
	})
	if err == nil || result != nil {
		t.Fatalf("case-insensitive provider collision admitted: %v", err)
	}
}

type unreadableConfigFS struct{ source fs.FS }

func (u unreadableConfigFS) Open(name string) (fs.File, error) {
	if name == "configs/a.json" {
		return nil, fs.ErrPermission
	}
	return u.source.Open(name)
}

func TestCatalogRetainsSourceReadFailure(t *testing.T) {
	source := unreadableConfigFS{source: fstest.MapFS{"configs/a.json": {Data: []byte(`{}`)}}}
	if _, err := loadCatalog(source); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("source failure lost: %v", err)
	}
}
