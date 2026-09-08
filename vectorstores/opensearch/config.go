package opensearch

import (
	"cmp"
	"errors"
	"fmt"

	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
)

// Provider is the stable backend name for host-side attribution.
const Provider = "OpenSearch"

// Exported defaults keep constructor behavior visible and overridable.
const (
	DefaultIndexName      = "scope-vector-index"
	DefaultEmbeddingField = "embedding"
	DefaultContentField   = "content"
	DefaultMetadataField  = "metadata"
	DefaultSpaceType      = SpaceTypeCosine
	DefaultEngine         = EngineLucene
	DefaultMethodName     = "hnsw"
)

// SpaceType selects the vector similarity space recorded in an OpenSearch
// knn_vector mapping. Because OpenSearch converts each space to a different
// raw-score representation, Store also uses this value to normalize scores
// into Core's provider-neutral contract.
type SpaceType string

// The metric is a closed vocabulary because score direction and threshold
// semantics depend on it: the same raw number means "near" under one metric and
// "far" under another, so an unrecognized value must be rejected rather than
// guessed.
const (
	SpaceTypeCosine SpaceType = "cosinesimil"
	SpaceTypeL2     SpaceType = "l2"
	SpaceTypeIP     SpaceType = "innerproduct"
	SpaceTypeL1     SpaceType = "l1"
	SpaceTypeLInf   SpaceType = "linf"
)

func (s SpaceType) Valid() bool {
	switch s {
	case SpaceTypeCosine, SpaceTypeL2, SpaceTypeIP, SpaceTypeL1, SpaceTypeLInf:
		return true
	default:
		return false
	}
}

func (s SpaceType) String() string { return string(s) }

func (s SpaceType) score(raw float64) (vectorstore.Score, error) {
	if s != SpaceTypeIP {
		return vectorstore.ScoreFromValue(raw), nil
	}

	// OpenSearch encodes positive inner products as product+1 and non-positive
	// products as 1/(1-product), so Core must recover the product before applying
	// its unbounded inner-product normalization.
	var product float64
	switch {
	case raw > 1:
		product = raw - 1
	case raw > 0:
		product = 1 - 1/raw
	default:
		// Both branches of that encoding are strictly positive, so a
		// non-positive score did not come from it. Reading it as a Core score
		// would be inventing a ranking out of a number this store cannot
		// explain — the recovery above is only as good as the formula it
		// inverts, and this is where a changed formula shows up.
		return 0, fmt.Errorf(
			"opensearch: inner-product score %v is outside the positive range OpenSearch documents for that space", raw)
	}
	return vectorstore.ScoreFromInnerProduct(product), nil
}

// Engine identifies the ANN implementation stored in an OpenSearch index
// mapping. Lucene is available in OpenSearch core; NMSLib and Faiss require
// compatible server plugins and unlock space/method combinations Lucene does
// not support.
type Engine string

// These are the provider values this adapter recognizes.
const (
	EngineLucene Engine = "lucene"
	EngineNMSLib Engine = "nmslib"
	EngineFaiss  Engine = "faiss"
)

func (e Engine) Valid() bool {
	switch e {
	case EngineLucene, EngineNMSLib, EngineFaiss:
		return true
	default:
		return false
	}
}

func (e Engine) String() string { return string(e) }

// StoreConfig defines one OpenSearch index binding. Field names and ANN
// settings become persistent index-schema policy, while Client, EmbeddingModel,
// and DocumentBatcher are runtime collaborators retained by Store.
type StoreConfig struct {
	// Client is the typed OpenSearch transport.
	Client *opensearchapi.Client

	// IndexName names the OpenSearch index. An empty value selects
	// [DefaultIndexName].
	IndexName string

	// EmbeddingField is the knn_vector field name. An empty value selects
	// [DefaultEmbeddingField].
	EmbeddingField string

	// ContentField stores document text. An empty value selects
	// [DefaultContentField].
	ContentField string

	// MetadataField owns document metadata. An empty value selects
	// [DefaultMetadataField]; OpenSearch filters therefore address metadata
	// beneath this field rather than flattening it into the document root.
	MetadataField string

	// EmbeddingModel produces vectors for indexed documents and search queries.
	EmbeddingModel embedding.Model

	// DocumentBatcher bounds each OpenSearch bulk request.
	DocumentBatcher vectorstore.Batcher

	// Dimensions fixes the knn_vector width when creating an index. Zero defers
	// discovery to EmbeddingModel, but only when the index must be created.
	Dimensions int

	// SpaceType selects the index similarity space. An empty value selects
	// [SpaceTypeCosine].
	SpaceType SpaceType

	// Engine selects the index ANN implementation. An empty value selects
	// [EngineLucene].
	Engine Engine

	// MethodName selects the ANN method. An empty value selects hnsw; ivf is
	// valid only with [EngineFaiss].
	MethodName string

	// InitializeSchema permits NewStore to create a missing index. When false,
	// a missing index is reported as [ErrIndexMissing].
	InitializeSchema bool
}

func (s StoreConfig) Validate() error {
	s.applyDefaults()
	if s.Client == nil {
		return errors.New("opensearch: client is required")
	}
	if lo.IsNil(s.EmbeddingModel) {
		return errors.New("opensearch: embedding model is required")
	}
	if lo.IsNil(s.DocumentBatcher) {
		return errors.New("opensearch: document batcher is required")
	}
	if s.Dimensions < 0 {
		return errors.New("opensearch: dimensions must be non-negative")
	}
	if !s.SpaceType.Valid() {
		return fmt.Errorf("opensearch: unsupported space type %q", s.SpaceType)
	}
	if !s.Engine.Valid() {
		return fmt.Errorf("opensearch: unsupported engine %q", s.Engine)
	}
	switch s.MethodName {
	case "hnsw":
	case "ivf":
		if s.Engine != EngineFaiss {
			return fmt.Errorf("opensearch: method %q requires the Faiss engine", s.MethodName)
		}
	default:
		return fmt.Errorf("opensearch: unsupported method name %q", s.MethodName)
	}
	if s.Engine == EngineLucene && (s.SpaceType == SpaceTypeL1 || s.SpaceType == SpaceTypeLInf) {
		return fmt.Errorf("opensearch: Lucene does not support space type %q", s.SpaceType)
	}
	return s.validateFieldLayout()
}

func (s StoreConfig) validateFieldLayout() error {
	fields := []struct {
		name  string
		value string
	}{
		{name: "ContentField", value: s.ContentField},
		{name: "EmbeddingField", value: s.EmbeddingField},
		{name: "MetadataField", value: s.MetadataField},
	}
	seen := make(map[string]string, len(fields))
	for _, field := range fields {
		if owner, duplicate := seen[field.value]; duplicate {
			return fmt.Errorf("opensearch: %s and %s both use field %q", owner, field.name, field.value)
		}
		seen[field.value] = field.name
	}
	return nil
}

func (s *StoreConfig) applyDefaults() {
	s.IndexName = cmp.Or(s.IndexName, DefaultIndexName)
	s.EmbeddingField = cmp.Or(s.EmbeddingField, DefaultEmbeddingField)
	s.ContentField = cmp.Or(s.ContentField, DefaultContentField)
	s.MetadataField = cmp.Or(s.MetadataField, DefaultMetadataField)
	s.SpaceType = cmp.Or(s.SpaceType, DefaultSpaceType)
	s.Engine = cmp.Or(s.Engine, DefaultEngine)
	s.MethodName = cmp.Or(s.MethodName, DefaultMethodName)
}
