package weaviate

import (
	"fmt"
	"math"
	"strings"

	"github.com/samber/lo"
	weaviateclient "github.com/weaviate/weaviate-go-client/v5/weaviate"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
)

const (
	Provider             = "Weaviate"
	fieldContent         = "content"
	fieldMetadata        = "metadata"
	additionalID         = "id"
	additionalDistance   = "distance"
	additionalScore      = "score"
	additionalVector     = "vector"
	metadataScanPageSize = 256
	distanceCosine       = "cosine"
	distanceDot          = "dot"
	distanceL2Squared    = "l2-squared"
	distanceHamming      = "hamming"
	distanceManhattan    = "manhattan"
)

type StoreConfig struct {
	Client          *weaviateclient.Client
	ClassName       string
	EmbeddingModel  embedding.Model
	DocumentBatcher vectorstore.Batcher
	// HybridAlpha selects native query fusion; nil preserves the native default.
	HybridAlpha *float32
}

func (s StoreConfig) Validate() error {
	if s.Client == nil {
		return ErrMissingClient
	}
	if s.ClassName == "" || strings.TrimSpace(s.ClassName) != s.ClassName {
		return ErrMissingClassName
	}
	if lo.IsNil(s.EmbeddingModel) {
		return ErrMissingEmbeddingModel
	}
	if lo.IsNil(s.DocumentBatcher) {
		return ErrMissingDocumentBatcher
	}
	if s.HybridAlpha != nil && (math.IsNaN(float64(*s.HybridAlpha)) || *s.HybridAlpha < 0 || *s.HybridAlpha > 1) {
		return fmt.Errorf("weaviate: HybridAlpha must be finite and in [0,1]")
	}
	return nil
}
