package redis

import (
	"cmp"
	"errors"
	"fmt"
	"strings"

	goredis "github.com/redis/go-redis/v9"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
)

// Provider is the stable backend name for host-side attribution.
const Provider = "Redis"

// Exported defaults keep constructor behavior visible and overridable.
const (
	DefaultIndexName         = "scope-vector-index"
	DefaultKeyPrefix         = "embedding:"
	DefaultContentField      = "content"
	DefaultEmbeddingField    = "embedding"
	DefaultMetadataJSONField = "metadata_json"
	DefaultMetadataPrefix    = "" // empty: metadata keys land at top level of the HASH
	DefaultDistanceMetric    = DistanceCosine
	DefaultIndexAlgorithm    = AlgorithmHNSW
	DefaultHNSWM             = 16
	DefaultHNSWEFConstruct   = 200
	DefaultHNSWEFRuntime     = 10
	distanceFieldName        = "__vector_distance"
	vectorParamName          = "scope_query_vec"
)

// DistanceMetric selects the similarity function used by the
// RediSearch vector index.
type DistanceMetric string

const (
	// DistanceCosine — cosine distance, range [0, 2]. The store
	// transforms it into a [0, 1] similarity score where higher is
	// more similar.
	DistanceCosine DistanceMetric = "COSINE"

	// DistanceL2 — Euclidean distance, range [0, ∞).
	DistanceL2 DistanceMetric = "L2"

	// DistanceIP — inner product. RediSearch returns the inner
	// product itself; the store maps it onto [0, 1] for unit-norm
	// vectors via (ip+1)/2.
	DistanceIP DistanceMetric = "IP"
)

func (d DistanceMetric) Valid() bool {
	switch d {
	case DistanceCosine, DistanceL2, DistanceIP:
		return true
	default:
		return false
	}
}

func (d DistanceMetric) String() string { return string(d) }

func (d DistanceMetric) score(distance float64) vectorstore.Score {
	switch d {
	case DistanceL2:
		return vectorstore.ScoreFromDistance(distance)
	case DistanceIP:
		// Redis defines IP distance as 1-dot-product.
		return vectorstore.ScoreFromOneMinusInnerProductDistance(distance)
	case DistanceCosine:
		fallthrough
	default:
		return vectorstore.ScoreFromCosineDistance(distance)
	}
}

// IndexAlgorithm selects the RediSearch vector indexing algorithm.
type IndexAlgorithm string

const (
	// AlgorithmHNSW — hierarchical navigable small-world graph.
	// Default; best query performance.
	AlgorithmHNSW IndexAlgorithm = "HNSW"

	// AlgorithmFlat — exhaustive (brute-force) search. Useful for
	// small collections where build / memory cost matters more than
	// query latency.
	AlgorithmFlat IndexAlgorithm = "FLAT"
)

func (i IndexAlgorithm) Valid() bool {
	return i == AlgorithmHNSW || i == AlgorithmFlat
}

func (i IndexAlgorithm) String() string { return string(i) }

// MetadataFieldType names the RediSearch schema field types the store
// understands. Callers declare these up-front so the filter visitor
// can validate field names and pick the right query syntax.
type MetadataFieldType string

const (
	// FieldTag — RediSearch TAG field. Exact-match on categorical
	// data; supports IN / != via "|" join and "-" prefix.
	FieldTag MetadataFieldType = "TAG"

	// FieldText — full-text indexed field.
	FieldText MetadataFieldType = "TEXT"

	// FieldNumeric — numeric range field.
	FieldNumeric MetadataFieldType = "NUMERIC"
)

func (m MetadataFieldType) Valid() bool {
	switch m {
	case FieldTag, FieldText, FieldNumeric:
		return true
	default:
		return false
	}
}

func (m MetadataFieldType) String() string { return string(m) }

func (m MetadataFieldType) searchFieldType() (goredis.SearchFieldType, bool) {
	switch m {
	case FieldNumeric:
		return goredis.SearchFieldTypeNumeric, true
	case FieldText:
		return goredis.SearchFieldTypeText, true
	case FieldTag:
		return goredis.SearchFieldTypeTag, true
	default:
		return 0, false
	}
}

// MetadataField declares one filterable metadata key. the framework's
// builder calls this a "MetadataField".
type MetadataField struct {
	// Name is the HASH field / JSON key that holds the value.
	Name string

	// Type controls the RediSearch index field type. See
	// [FieldTag] / [FieldText] / [FieldNumeric].
	Type MetadataFieldType

	// Sortable, when true, marks the field SORTABLE in the schema.
	Sortable bool
}

func (m MetadataField) Validate() error {
	if m.Name == "" || strings.TrimSpace(m.Name) != m.Name {
		return errors.New("name is required and must not have surrounding whitespace")
	}
	if !m.Type.Valid() {
		return fmt.Errorf("type %q is unsupported", m.Type)
	}
	return nil
}

// StoreConfig contains configuration options for the Redis vector
// store.
type StoreConfig struct {
	// Client is the go-redis client (single, cluster, or sentinel).
	// Required.
	Client goredis.UniversalClient

	// IndexName names the RediSearch index. Optional: defaults to
	// [DefaultIndexName].
	IndexName string

	// KeyPrefix is the Redis-key prefix the index attaches to —
	// every stored HASH lives at `<KeyPrefix><id>`. Optional:
	// defaults to [DefaultKeyPrefix].
	KeyPrefix string

	// ContentField is the HASH field that holds the original
	// document text. Optional: defaults to [DefaultContentField].
	ContentField string

	// EmbeddingField is the HASH field that holds the binary
	// FLOAT32 vector. Optional: defaults to [DefaultEmbeddingField].
	EmbeddingField string

	// MetadataJSONField is the HASH field that holds the document's metadata
	// as JSON. Optional: defaults to [DefaultMetadataJSONField].
	//
	// It is deliberately absent from the index schema. RediSearch indexes a
	// HASH field's text as its declared type, so a declared field has to hold
	// the value in the form the index expects — which is why the declared
	// fields below cannot also be the metadata of record. A number written to
	// a NUMERIC field reads back as a float64 and everything else as a string,
	// and an undeclared key has no field to read at all, so reconstructing
	// metadata from the index returned a document that differs from the one
	// that was written. This field is the record; the declared fields are the
	// index projection of it.
	MetadataJSONField string

	// MetadataFields enumerates every metadata key the index should
	// understand. Only declared fields can appear in a filter
	// expression — the store rejects unknown identifiers up-front to
	// preclude query injection.
	MetadataFields []MetadataField

	// EmbeddingModel produces vectors for the documents. Required.
	EmbeddingModel embedding.Model

	// DocumentBatcher batches documents before upsert. Required.
	DocumentBatcher vectorstore.Batcher

	// Dimensions sets the vector width registered with a new index. When zero
	// and InitializeSchema is true, the store probes EmbeddingModel.
	Dimensions int

	// DistanceMetric selects the vector similarity function.
	// Optional: defaults to [DistanceCosine].
	DistanceMetric DistanceMetric

	// IndexAlgorithm selects HNSW vs FLAT. Optional: defaults to
	// [AlgorithmHNSW].
	IndexAlgorithm IndexAlgorithm

	// HNSWM / HNSWEFConstruct / HNSWEFRuntime tune the HNSW index.
	// Each defaults via [DefaultHNSW*] when zero. Ignored when
	// IndexAlgorithm is FLAT.
	HNSWM           int
	HNSWEFConstruct int
	HNSWEFRuntime   int

	// InitializeSchema, when true, runs FT.CREATE on construction if
	// the index doesn't already exist. When false, the store assumes
	// the index is pre-provisioned.
	InitializeSchema bool
}

func (s StoreConfig) Validate() error {
	s.applyDefaults()
	if s.Client == nil {
		return errors.New("redis: Client is required")
	}
	if lo.IsNil(s.EmbeddingModel) {
		return errors.New("redis: EmbeddingModel is required")
	}
	if lo.IsNil(s.DocumentBatcher) {
		return errors.New("redis: DocumentBatcher is required")
	}
	if s.Dimensions < 0 {
		return errors.New("redis: Dimensions must be >= 0")
	}
	if !s.DistanceMetric.Valid() {
		return fmt.Errorf("redis: unsupported DistanceMetric %q", s.DistanceMetric)
	}
	if !s.IndexAlgorithm.Valid() {
		return fmt.Errorf("redis: unsupported IndexAlgorithm %q", s.IndexAlgorithm)
	}
	if s.IndexAlgorithm == AlgorithmHNSW &&
		(s.HNSWM <= 0 || s.HNSWEFConstruct <= 0 || s.HNSWEFRuntime <= 0) {
		return errors.New("redis: HNSW parameters must all be > 0")
	}
	// Metadata keys land in the same HASH as these fields, so a name reused
	// here would have two writers and the last one would win.
	reserved := map[string]string{
		s.ContentField:      "ContentField",
		s.EmbeddingField:    "EmbeddingField",
		s.MetadataJSONField: "MetadataJSONField",
	}
	if len(reserved) != 3 {
		return fmt.Errorf(
			"redis: ContentField %q, EmbeddingField %q and MetadataJSONField %q must name three distinct HASH fields",
			s.ContentField, s.EmbeddingField, s.MetadataJSONField)
	}
	fieldNames := make(map[string]struct{}, len(s.MetadataFields))
	for index, field := range s.MetadataFields {
		if err := field.Validate(); err != nil {
			return fmt.Errorf("redis: MetadataFields[%d]: %w", index, err)
		}
		if _, duplicate := fieldNames[field.Name]; duplicate {
			return fmt.Errorf("redis: MetadataFields contains duplicate field %q", field.Name)
		}
		if owner, taken := reserved[field.Name]; taken {
			return fmt.Errorf("redis: MetadataFields[%d] %q collides with %s", index, field.Name, owner)
		}
		fieldNames[field.Name] = struct{}{}
	}
	return nil
}

// applyDefaults fills zero fields with documented defaults.
func (s *StoreConfig) applyDefaults() {
	s.IndexName = cmp.Or(s.IndexName, DefaultIndexName)
	s.KeyPrefix = cmp.Or(s.KeyPrefix, DefaultKeyPrefix)
	s.ContentField = cmp.Or(s.ContentField, DefaultContentField)
	s.EmbeddingField = cmp.Or(s.EmbeddingField, DefaultEmbeddingField)
	s.MetadataJSONField = cmp.Or(s.MetadataJSONField, DefaultMetadataJSONField)
	s.DistanceMetric = cmp.Or(s.DistanceMetric, DefaultDistanceMetric)
	s.IndexAlgorithm = cmp.Or(s.IndexAlgorithm, DefaultIndexAlgorithm)
	if s.IndexAlgorithm == AlgorithmHNSW {
		if s.HNSWM == 0 {
			s.HNSWM = DefaultHNSWM
		}
		if s.HNSWEFConstruct == 0 {
			s.HNSWEFConstruct = DefaultHNSWEFConstruct
		}
		if s.HNSWEFRuntime == 0 {
			s.HNSWEFRuntime = DefaultHNSWEFRuntime
		}
	}
}
