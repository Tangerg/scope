package elastic

import (
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"

	"github.com/Tangerg/scope/core/vectorstore"
)

type bulkResponse struct {
	Errors *bool      `json:"errors"`
	Items  []bulkItem `json:"items"`
}

func (b bulkResponse) validate(operation Operation, index string, ids []string, allowMissing bool) error {
	if b.Errors == nil || len(b.Items) != len(ids) {
		return errors.New("elastic: native bulk did not acknowledge every document")
	}
	for i, item := range b.Items {
		result := item.result(operation, index, ids[i])
		if result == nil {
			return errors.New("elastic: native bulk acknowledgment differs from the requested operation or identity")
		}
		if !result.accepted(allowMissing) {
			return fmt.Errorf("elastic: native bulk %s failed for %q: status=%d, error=%s", operation, ids[i], *result.Status, result.Error)
		}
	}
	if *b.Errors {
		return errors.New("elastic: native bulk reported an unexplained failure")
	}
	return nil
}

// Item evidence is usable only inside a coherent envelope. Contradictory
// summaries invalidate attribution instead of arbitrating two sources of truth.
func (b bulkResponse) writeFailure(index string, ids []string, cause error) *vectorstore.WriteError {
	var w vectorstore.WriteOutcome
	if b.Errors == nil || len(b.Items) > len(ids) {
		w.Unconfirmed = ids
		return vectorstore.NewWriteError(cause, w)
	}
	bad := false
	for i, id := range ids {
		if i >= len(b.Items) {
			w.Unconfirmed = append(w.Unconfirmed, id)
			continue
		}
		r := b.Items[i].result(Index, index, id)
		if r == nil {
			w.Unconfirmed = append(w.Unconfirmed, id)
			continue
		}
		switch {
		case r.accepted(false):
			w.Acknowledged = append(w.Acknowledged, id)
		case *r.Status >= http.StatusBadRequest && *r.Status < http.StatusInternalServerError && r.Error != nil:
			w.Rejected = append(w.Rejected, id)
			bad = true
		default:
			w.Unconfirmed = append(w.Unconfirmed, id)
			bad = true
		}
	}
	if !*b.Errors && bad || *b.Errors && len(w.Acknowledged) == len(ids) {
		return vectorstore.NewWriteError(cause, vectorstore.WriteOutcome{Unconfirmed: ids})
	}
	return vectorstore.NewWriteError(cause, w)
}

type bulkItemResult struct {
	Index  string          `json:"_index"`
	ID     string          `json:"_id"`
	Status *int            `json:"status"`
	Error  json.RawMessage `json:"error,omitzero"`
}

func (b bulkItemResult) accepted(allowMissing bool) bool {
	if b.Status == nil || b.Error != nil {
		return false
	}
	return *b.Status >= http.StatusOK && *b.Status < http.StatusMultipleChoices || allowMissing && *b.Status == http.StatusNotFound
}

type bulkItem struct {
	Index   *bulkItemResult `json:"index,omitzero"`
	Delete  *bulkItemResult `json:"delete,omitzero"`
	Unknown jsontext.Value  `json:",unknown"`
}

// UnmarshalJSON commits only a complete item, so a truncated object cannot
// acknowledge a write merely because its status member arrived before EOF.
func (b *bulkItem) UnmarshalJSON(raw []byte) error {
	type wire bulkItem
	var decoded wire
	if err := jsonv2.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*b = bulkItem(decoded)
	return nil
}

func (b bulkItem) result(operation Operation, index, id string) *bulkItemResult {
	if len(b.Unknown) != 0 || (b.Index == nil) == (b.Delete == nil) {
		return nil
	}
	var result *bulkItemResult
	switch operation {
	case Index:
		result = b.Index
	case Delete:
		result = b.Delete
	}
	if result == nil || result.ID != id || result.Index != index || result.Status == nil {
		return nil
	}
	return result
}
