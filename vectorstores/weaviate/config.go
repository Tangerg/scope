package weaviate

import (
	"fmt"
	"math"

	"github.com/Tangerg/scope/core/vectorstore"
)

const (
	Provider          = "Weaviate"
	fieldContent      = "content"
	fieldMetadata     = "metadata"
	fieldTerms        = "metadataTerms"
	vectorName        = "embedding"
	distanceCosine    = "cosine"
	distanceDot       = "dot"
	distanceL2Squared = "l2-squared"
	distanceHamming   = "hamming"
	distanceManhattan = "manhattan"
)

type StoreConfig struct {
	Client     *Client
	Collection string
	Space      vectorstore.SpaceID
	// HybridAlpha selects native query fusion; nil preserves the native default.
	HybridAlpha *float32
}

func (s StoreConfig) Validate() error {
	if !validCollectionName(s.Collection) {
		return ErrMissingCollection
	}
	if err := s.Space.Validate(); err != nil {
		return err
	}
	if s.HybridAlpha != nil && (math.IsNaN(float64(*s.HybridAlpha)) || *s.HybridAlpha < 0 || *s.HybridAlpha > 1) {
		return fmt.Errorf("weaviate: HybridAlpha must be finite and in [0,1]")
	}
	return s.Client.validate()
}
