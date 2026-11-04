package vectorstore

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/Tangerg/scope/core/document"
)

// SpaceID identifies the Host's immutable pairing of document and query
// encoders (including preprocessing and model revisions). Coordinates do not
// reveal this identity, and equal dimensions do not establish compatibility.
type SpaceID string

// SpaceStorageKeyPrefix reserves a namespace so adapters can detect every
// declared binding, including a second constraint for a conflicting space.
const SpaceStorageKeyPrefix = "sv_"

var ErrIncompatibleSpace = errors.New("vectorstore: incompatible vector space")
var ErrUnsupportedFilter = errors.New("vectorstore: metadata predicate cannot be executed natively")

// JSON null must not invent a measured zero coordinate.
var coordinateJSON = jsonv2.UnmarshalFunc(func(data []byte, target *[]float64) error {
	var decoded []*float64
	if err := jsonv2.Unmarshal(data, &decoded); err != nil {
		return err
	}
	values := make([]float64, len(decoded))
	for index, value := range decoded {
		if value == nil {
			return fmt.Errorf("%w: coordinate %d must be a number", ErrInvalidRequest, index)
		}
		values[index] = *value
	}
	*target = values
	return nil
})

// StorageKey is a compact SQL-identifier-safe projection for physical schemas
// with length or character restrictions. Its input is the declared Host binding,
// not the model name, embedding coordinates, or executable code.
func (s SpaceID) StorageKey() string {
	digest := sha256.Sum256([]byte(s))
	return SpaceStorageKeyPrefix + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(digest[:]))
}

func (s SpaceID) Validate() error {
	if s == "" || !utf8.ValidString(string(s)) || strings.TrimSpace(string(s)) != string(s) {
		return fmt.Errorf("%w: a nonblank, unpadded UTF-8 space ID is required", ErrIncompatibleSpace)
	}
	return nil
}

// Require rejects requests for a different physical index binding.
func (s SpaceID) Require(expected SpaceID) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if s != expected {
		return fmt.Errorf("%w: requested %q, bound %q", ErrIncompatibleSpace, s, expected)
	}
	return nil
}

// Record associates an indexed document with already-computed coordinates.
type Record struct {
	Document *document.Document `json:"document"`
	Values   []float64          `json:"values"`
}

func (r Record) Validate() error {
	if r.Document == nil {
		return fmt.Errorf("%w: missing document", ErrInvalidDocument)
	}
	if err := validatePresentDocument(r.Document); err != nil {
		return err
	}
	return validateCoordinates(r.Values)
}

func (r Record) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	type wire Record
	return jsonv2.Marshal(wire(r))
}
func (r *Record) UnmarshalJSON(data []byte) error {
	if r == nil {
		return ErrInvalidRequest
	}
	type wire Record
	var decoded wire
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true), jsonv2.WithUnmarshalers(coordinateJSON)); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	candidate := Record(decoded)
	if err := candidate.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	*r = candidate
	return nil
}

// Batch contains records from exactly one encoder space. Upsert borrows these
// records for the duration of the call; it must not retain mutable input.
type Batch struct {
	Space   SpaceID  `json:"space"`
	Records []Record `json:"records"`
}

func (b *Batch) ValidateFor(space SpaceID) error {
	if b == nil || len(b.Records) == 0 {
		return fmt.Errorf("%w: empty vector batch", ErrInvalidRequest)
	}
	if err := b.Space.Require(space); err != nil {
		return err
	}
	seen := make(map[string]int, len(b.Records))
	for i, record := range b.Records {
		if err := record.Validate(); err != nil {
			return fmt.Errorf("%w: records[%d]: %w", ErrInvalidRequest, i, err)
		}
		if err := uniqueID(seen, record.Document.ID, i); err != nil {
			return err
		}
		if len(record.Values) != len(b.Records[0].Values) {
			return fmt.Errorf("%w: mixed vector dimensions", ErrInvalidRequest)
		}
	}
	return nil
}

// Query carries precomputed coordinates. LexicalText is only meaningful in
// native hybrid mode: stores must not silently run a different search.
type Query struct {
	Space       SpaceID       `json:"space"`
	Values      []float64     `json:"values"`
	LexicalText string        `json:"lexical_text,omitzero"`
	Options     SearchOptions `json:"options"`
}

func (q *Query) ValidateFor(space SpaceID) error {
	if q == nil {
		return fmt.Errorf("%w: nil vector query", ErrInvalidRequest)
	}
	if err := q.Space.Require(space); err != nil {
		return err
	}
	if err := validateCoordinates(q.Values); err != nil {
		return err
	}
	if err := q.Options.Validate(); err != nil {
		return err
	}
	if !utf8.ValidString(q.LexicalText) || q.Options.Mode == SearchModeHybrid && strings.TrimSpace(q.LexicalText) == "" || q.Options.Mode != SearchModeHybrid && q.LexicalText != "" {
		return fmt.Errorf("%w: lexical text is required only for hybrid search", ErrInvalidRequest)
	}
	return nil
}

func validateCoordinates(values []float64) error {
	if len(values) == 0 {
		return fmt.Errorf("%w: missing coordinates", ErrInvalidRequest)
	}
	for i, v := range values {
		if math.IsInf(v, 0) || math.IsNaN(v) {
			return fmt.Errorf("%w: coordinate %d is not finite", ErrInvalidRequest, i)
		}
	}
	return nil
}

// Upserter writes precomputed vectors without invoking an embedding model.
type Upserter interface {
	Upsert(context.Context, *Batch) error
}

// Querier ranks precomputed vectors without invoking an embedding model.
type Querier interface {
	Query(context.Context, *Query) (*SearchResponse, error)
}

// ValidateResponse detects protocol violations; it never fixes incorrect
// membership by filtering a truncated top-K result.
func (q *Query) ValidateResponse(result *SearchResponse) error {
	if q == nil {
		return fmt.Errorf("%w: nil vector query", ErrInvalidRequest)
	}
	if err := q.ValidateFor(q.Space); err != nil {
		return err
	}
	return result.validateOptions(q.Options)
}

func (b Batch) MarshalJSON() ([]byte, error) {
	if err := b.ValidateFor(b.Space); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	type wire Batch
	return jsonv2.Marshal(wire(b))
}
func (b *Batch) UnmarshalJSON(data []byte) error {
	if b == nil {
		return ErrInvalidRequest
	}
	type wire Batch
	var decoded wire
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	candidate := Batch(decoded)
	if err := candidate.ValidateFor(candidate.Space); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	*b = candidate
	return nil
}

func (q Query) MarshalJSON() ([]byte, error) {
	if err := q.ValidateFor(q.Space); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	type wire Query
	return jsonv2.Marshal(wire(q))
}
func (q *Query) UnmarshalJSON(data []byte) error {
	if q == nil {
		return ErrInvalidRequest
	}
	type wire Query
	var decoded wire
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true), jsonv2.WithUnmarshalers(coordinateJSON)); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	candidate := Query(decoded)
	if err := candidate.ValidateFor(candidate.Space); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	*q = candidate
	return nil
}
