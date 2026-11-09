package elastic

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"slices"
	"testing"
)

func TestBulkAcknowledgmentsCannotInventSuccess(t *testing.T) {
	for _, raw := range []string{
		`{"items":[]}`,
		`{"errors":false,"items":[]}`,
		`{"errors":false,"items":[{"index":{"_index":"documents","_id":"other","status":201}}]}`,
		`{"errors":false,"items":[{"index":{"_index":"other","_id":"one","status":201}}]}`,
		`{"errors":false,"items":[{"index":{"_index":"documents","_id":"one","status":400,"error":{"reason":"mapping"}}}]}`,
		`{"errors":true,"items":[{"index":{"_index":"documents","_id":"one","status":201}}]}`,
	} {
		var response bulkResponse
		if err := jsonv2.Unmarshal([]byte(raw), &response); err != nil {
			t.Fatal(err)
		}
		if err := response.validate(Index, "documents", []string{"one"}, false); err == nil {
			t.Fatalf("invalid acknowledgment accepted: %s", raw)
		}
	}
	var missing bulkResponse
	if err := jsonv2.Unmarshal([]byte(`{"errors":false,"items":[{"delete":{"_index":"documents","_id":"one","status":404}}]}`), &missing); err != nil {
		t.Fatal(err)
	}
	if err := missing.validate(Delete, "documents", []string{"one"}, true); err != nil {
		t.Fatal(err)
	}
	if err := missing.validate(Delete, "documents", []string{"one"}, false); err == nil {
		t.Fatal("conditional deletion accepted a vanished snapshot member")
	}
}

func TestWriteFailureRetainsOnlyAttributableItemAcknowledgments(t *testing.T) {
	ids := []string{"accepted", "denied", "unknown"}
	resp := bulkResponse{Errors: new(true), Items: []bulkItem{
		{Index: &bulkItemResult{ID: "accepted", Index: "docs", Status: new(201)}},
		{Index: &bulkItemResult{ID: "denied", Index: "docs", Status: new(409), Error: []byte(`{"type":"conflict"}`)}},
	}}
	cause := errors.New("partial bulk response")
	w := resp.writeFailure("docs", ids, cause)
	if !errors.Is(w, cause) || !slices.Equal(w.Outcome().Acknowledged, ids[:1]) || !slices.Equal(w.Outcome().Rejected, ids[1:2]) || !slices.Equal(w.Outcome().Unconfirmed, ids[2:]) {
		t.Fatalf("incorrect write evidence: %+v", *w)
	}
	resp.Errors = new(false)
	w = resp.writeFailure("docs", ids, cause)
	if len(w.Outcome().Acknowledged) != 0 || len(w.Outcome().Rejected) != 0 || !slices.Equal(w.Outcome().Unconfirmed, ids) {
		t.Fatalf("contradictory bulk reply invented success: %+v", *w)
	}
}
