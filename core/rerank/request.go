package rerank

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"slices"
	"strings"
)

// Request is one reranking call. Result indices address Documents in this
// immutable order.
type Request struct {
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	Options   Options  `json:"options,omitzero"`
}

// NewRequest binds the query and its candidate set together because a rerank
// result is addressed by position into that exact slice. Accepting them
// separately would let a caller score one list and index into another.
func NewRequest(query string, documents []string) (*Request, error) {
	request := &Request{Query: query, Documents: slices.Clone(documents)}
	if err := request.Validate(); err != nil {
		return nil, fmt.Errorf("rerank: create request: %w", err)
	}
	return request, nil
}

func (r *Request) Validate() error {
	if r == nil {
		return fmt.Errorf("%w: nil request", ErrInvalidRequest)
	}
	if strings.TrimSpace(r.Query) == "" {
		return fmt.Errorf("%w: query must not be blank", ErrInvalidRequest)
	}
	if len(r.Documents) == 0 {
		return fmt.Errorf("%w: documents must contain at least one entry", ErrInvalidRequest)
	}
	for index, document := range r.Documents {
		if strings.TrimSpace(document) == "" {
			return fmt.Errorf("%w: documents[%d] must not be blank", ErrInvalidRequest, index)
		}
	}
	if err := r.Options.Validate(); err != nil {
		return fmt.Errorf("%w: options: %w", ErrInvalidRequest, err)
	}
	if r.Options.TopK > len(r.Documents) {
		return fmt.Errorf("%w: top K %d exceeds document count %d", ErrInvalidRequest, r.Options.TopK, len(r.Documents))
	}
	return nil
}

func (r Request) MarshalJSON() ([]byte, error) {
	if err := (&r).Validate(); err != nil {
		return nil, err
	}
	type wireRequest Request
	return json.Marshal(wireRequest(r))
}

func (r *Request) UnmarshalJSON(data []byte) error {
	if r == nil {
		return fmt.Errorf("%w: request receiver is nil", ErrInvalidRequest)
	}
	type wireRequest Request
	var decoded wireRequest
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode request: %w", ErrInvalidRequest, err)
	}
	candidate := Request(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*r = candidate
	return nil
}
