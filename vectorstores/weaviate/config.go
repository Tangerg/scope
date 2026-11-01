package weaviate

import (
	"fmt"
	"math"
	"strings"

	weaviateclient "github.com/weaviate/weaviate-go-client/v5/weaviate"

	"github.com/Tangerg/scope/core/vectorstore"
)

const (
	Provider           = "Weaviate"
	fieldContent       = "content"
	fieldMetadata      = "metadata"
	fieldTerms         = "metadataTerms"
	additionalID       = "id"
	additionalDistance = "distance"
	additionalScore    = "score"
	additionalVector   = "vector"
	distanceCosine     = "cosine"
	distanceDot        = "dot"
	distanceL2Squared  = "l2-squared"
	distanceHamming    = "hamming"
	distanceManhattan  = "manhattan"
)

type StoreConfig struct {
	Client    *weaviateclient.Client
	ClassName string
	Space     vectorstore.SpaceID
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
	if err := s.Space.Validate(); err != nil {
		return err
	}
	if s.HybridAlpha != nil && (math.IsNaN(float64(*s.HybridAlpha)) || *s.HybridAlpha < 0 || *s.HybridAlpha > 1) {
		return fmt.Errorf("weaviate: HybridAlpha must be finite and in [0,1]")
	}
	return nil
}
