package elastic

import (
	"encoding/json/jsontext"

	"github.com/Tangerg/scope/core/vectorstore"
)

// Engine is what distinguishes one Elastic-protocol search engine: how it
// declares an embedding field and which settings can change stored facts or
// results. Store owns the record layout, namespace and result window.
type Engine interface {
	// Name prefixes errors that only this engine's index can produce.
	Name() string
	// SettingNames lists the index settings Admit reads.
	SettingNames() []string
	// Admit derives the schema of an index whose record layout Store has
	// already admitted.
	Admit(embedding jsontext.Value, settings IndexSettings) (Schema, error)
}

// Schema is an engine's admission of one concrete index. Store re-admits the
// index before every mutation and query and compares the two schemas with ==,
// so implementations must be comparable values.
type Schema interface {
	// ValidateIndexedVector checks a vector the index stores or returns.
	ValidateIndexedVector(vector []float32) error
	// ValidateQueryVector checks a vector the index searches with.
	ValidateQueryVector(vector []float32) error
	// Score projects a native score into Core's normalized score.
	Score(raw float64) (vectorstore.Score, error)
	// CheckFilteredSearch reports why the index cannot restrict nearest
	// neighbors to a candidate set, before any native request.
	CheckFilteredSearch() error
	// SearchSyntax encodes the engine's members of a search body: a _source
	// projection that returns complete stored records and, when neighbors is
	// set, the nearest-neighbor clause.
	SearchSyntax(neighbors *Neighbors) (jsontext.Value, error)
}

// Neighbors asks for the K records nearest to Vector, restricted to IDs when
// IDs is not nil.
type Neighbors struct {
	Vector []float32
	K      int
	IDs    []string
}
