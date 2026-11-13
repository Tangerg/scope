package vectorstore

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vector"
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

// RequireWidth rejects request coordinates whose width differs from the width
// of the vectors a store holds: they cannot come from the space the store is
// bound to, although equal widths alone never establish that they do.
func RequireWidth[T float32 | float64](values vector.Coordinates[T], width int) error {
	if len(values) != width {
		return fmt.Errorf("%w: %d coordinates for native width %d", ErrIncompatibleSpace, len(values), width)
	}
	return nil
}

// Record associates an indexed document with already-computed coordinates.
type Record struct {
	Document *document.Document          `json:"document"`
	Values   vector.Coordinates[float64] `json:"values"`
}

func (r Record) Validate() error {
	if r.Document == nil {
		return fmt.Errorf("%w: missing document", ErrInvalidDocument)
	}
	if err := validatePresentDocument(r.Document); err != nil {
		return err
	}
	if err := r.Values.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	return nil
}

func (r Record) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	return jsonv2.Marshal(wireRecord(r))
}

func (r *Record) UnmarshalJSON(data []byte) error {
	if r == nil {
		return ErrInvalidRequest
	}
	var decoded wireRecord
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
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
	Space       SpaceID                     `json:"space"`
	Values      vector.Coordinates[float64] `json:"values"`
	LexicalText string                      `json:"lexical_text,omitzero"`
	Options     SearchOptions               `json:"options"`
}

func (q *Query) ValidateFor(space SpaceID) error {
	if q == nil {
		return fmt.Errorf("%w: nil vector query", ErrInvalidRequest)
	}
	if err := q.Space.Require(space); err != nil {
		return err
	}
	if err := q.Values.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	if err := q.Options.Validate(); err != nil {
		return err
	}
	if !utf8.ValidString(q.LexicalText) || q.Options.Mode == SearchModeHybrid && strings.TrimSpace(q.LexicalText) == "" || q.Options.Mode != SearchModeHybrid && q.LexicalText != "" {
		return fmt.Errorf("%w: lexical text is required only for hybrid search", ErrInvalidRequest)
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

// ValidateResponse holds a response to the options of a query its store
// already admitted with ValidateFor. It detects protocol violations and never
// fixes incorrect membership by filtering a truncated top-K result.
func (q *Query) ValidateResponse(result *SearchResponse) error {
	if q == nil {
		return fmt.Errorf("%w: nil vector query", ErrInvalidRequest)
	}
	return result.validateOptions(q.Options)
}

func (b Batch) MarshalJSON() ([]byte, error) {
	if err := b.ValidateFor(b.Space); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	return jsonv2.Marshal(wireBatch(b))
}

func (b *Batch) UnmarshalJSON(data []byte) error {
	if b == nil {
		return ErrInvalidRequest
	}
	var decoded wireBatch
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
	return jsonv2.Marshal(wireQuery(q))
}

func (q *Query) UnmarshalJSON(data []byte) error {
	if q == nil {
		return ErrInvalidRequest
	}
	var decoded wireQuery
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	candidate := Query(decoded)
	if err := candidate.ValidateFor(candidate.Space); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	*q = candidate
	return nil
}

type wireRecord Record

func (Record) JSONSchemaModel() any { return wireRecord{} }

type wireBatch Batch

func (Batch) JSONSchemaModel() any { return wireBatch{} }

type wireQuery Query

func (Query) JSONSchemaModel() any { return wireQuery{} }
