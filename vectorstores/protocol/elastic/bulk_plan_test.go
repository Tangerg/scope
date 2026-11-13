package elastic

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore"
)

func TestBulkReceiptSurvivesReadAndCloseFailures(t *testing.T) {
	closeErr := errors.New("close failed")
	callErr := errors.New("transport failed after receipt")
	const accepted = `{"index":{"_index":"documents","_id":"one","status":201}}`
	const second = `{"index":{"_index":"documents","_id":"two","status":201}}`
	for _, sample := range []struct {
		name    string
		reply   wireReply
		ack     []string
		unknown []string
	}{
		{name: "complete close", reply: wireReply{status: 200, body: `{"errors":false,"items":[` + accepted + `,` + second + `]}`, closeErr: closeErr}, ack: []string{"one", "two"}},
		{name: "complete read", reply: wireReply{status: 200, body: `{"errors":false,"items":[` + accepted + `,` + second + `]}`, readErr: io.ErrUnexpectedEOF}, ack: []string{"one", "two"}},
		{name: "response with call error", reply: wireReply{status: 200, body: `{"errors":false,"items":[` + accepted + `,` + second + `]}`, callErr: callErr}, ack: []string{"one", "two"}},
		{name: "truncated second item", reply: wireReply{status: 200, body: `{"errors":false,"items":[` + accepted + `,{"index":{"_index":"documents","_id":"two","status":201`, readErr: io.ErrUnexpectedEOF}, ack: []string{"one"}, unknown: []string{"two"}},
		{name: "contradictory summary", reply: wireReply{status: 200, body: `{"errors":true,"items":[` + accepted + `,` + second + `]}`, closeErr: closeErr}, unknown: []string{"one", "two"}},
		{name: "conflicting repeated summary", reply: wireReply{status: 200, body: `{"errors":false,"items":[` + accepted + `,` + second + `],"errors":true}`, closeErr: closeErr}, unknown: []string{"one", "two"}},
		{name: "missing HTTP envelope", reply: wireReply{body: `{"errors":false,"items":[` + accepted + `,` + second + `]}`, callErr: callErr}, unknown: []string{"one", "two"}},
		{name: "failed HTTP envelope", reply: wireReply{status: 503, body: `{"errors":false,"items":[` + accepted + `,` + second + `]}`, closeErr: closeErr}, unknown: []string{"one", "two"}},
	} {
		t.Run(sample.name, func(t *testing.T) {
			transport := &scriptedTransport{t: t, replies: []wireReply{sample.reply}}
			plan := bulkPlan(t, Index, 0)
			for _, id := range []string{"one", "two"} {
				if err := plan.Index(id, struct {
					Text string `json:"text"`
				}{Text: id}); err != nil {
					t.Fatal(err)
				}
			}
			err := plan.Publish(t.Context(), protocolClient(t, transport, 0))
			writeErr, ok := errors.AsType[*vectorstore.WriteError](err)
			if !ok {
				t.Fatalf("missing write evidence: %v", err)
			}
			outcome := writeErr.Outcome()
			if !slices.Equal(outcome.Acknowledged, sample.ack) || !slices.Equal(outcome.Unconfirmed, sample.unknown) {
				t.Fatalf("outcome=%+v", outcome)
			}
			for _, cause := range []error{sample.reply.readErr, sample.reply.closeErr, sample.reply.callErr} {
				if cause != nil && !errors.Is(err, cause) {
					t.Fatalf("lost %v: %v", cause, err)
				}
			}
			if !transport.bodies[0].closed {
				t.Fatal("receipt body leaked")
			}
		})
	}
}

func TestBulkPublicationPartitionsAndPreservesSegmentEvidence(t *testing.T) {
	const receipt = `{"errors":false,"items":[{"index":{"_index":"documents","_id":"one","status":201}}]}`
	transport := &scriptedTransport{t: t, replies: []wireReply{{status: 200, body: receipt}, {status: 200, body: `{`}}}
	plan := bulkPlan(t, Index, 100)
	for _, id := range []string{"one", "two", "three"} {
		if err := plan.Index(id, struct {
			Text string `json:"text"`
		}{Text: id}); err != nil {
			t.Fatal(err)
		}
	}
	err := plan.Publish(t.Context(), protocolClient(t, transport, 0))
	writeErr, ok := errors.AsType[*vectorstore.WriteError](err)
	if !ok {
		t.Fatalf("missing receipt: %v", err)
	}
	outcome := writeErr.Outcome()
	if !slices.Equal(outcome.Acknowledged, []string{"one"}) || !slices.Equal(outcome.Unconfirmed, []string{"two"}) || !slices.Equal(outcome.Unattempted, []string{"three"}) {
		t.Fatalf("segment evidence=%+v", outcome)
	}
	if len(transport.requests) != 2 || transport.requests[0].body != "{\"index\":{\"_index\":\"documents\",\"_id\":\"one\"}}\n{\"text\":\"one\"}\n" {
		t.Fatalf("publications=%+v", transport.requests)
	}
	if transport.requests[0].header.Get("Content-Type") != "application/x-ndjson" {
		t.Fatal("bulk framing media type changed")
	}
}

func TestDeletePlanOwnsVersionAndAbsenceSemantics(t *testing.T) {
	const missing = `{"errors":false,"items":[{"delete":{"_index":"documents","_id":"one","status":404}}]}`
	for _, conditional := range []bool{false, true} {
		t.Run(fmt.Sprint(conditional), func(t *testing.T) {
			transport := &scriptedTransport{t: t, replies: []wireReply{{status: http.StatusOK, body: missing}}}
			plan := bulkPlan(t, Delete, 0)
			var version *Version
			if conditional {
				version = &Version{SeqNo: 3, PrimaryTerm: 1}
			}
			if err := plan.Delete("one", version); err != nil {
				t.Fatal(err)
			}
			err := plan.Publish(t.Context(), protocolClient(t, transport, 0))
			if (err != nil) != conditional {
				t.Fatalf("conditional=%v error=%v", conditional, err)
			}
			if conditional && !strings.Contains(transport.requests[0].body, `"if_seq_no":3,"if_primary_term":1`) {
				t.Fatal("conditional version was lost")
			}
		})
	}
}

func TestBulkPlanRejectsInvalidAdmissionWithoutPublication(t *testing.T) {
	for _, config := range []BulkConfig{
		{Index: "documents", Operation: Index, MaxBytes: -1},
		{Index: "documents", Operation: "update"},
		{Index: "documents,*", Operation: Index},
	} {
		if plan, err := NewBulkPlan(config); plan != nil || err == nil {
			t.Fatalf("invalid plan=%v error=%v", plan, err)
		}
	}
	plan := bulkPlan(t, Index, 70)
	if err := plan.Index("one", true); err != nil {
		t.Fatal(err)
	}
	if err := plan.Index("one", true); !errors.Is(err, vectorstore.ErrDuplicateDocumentID) {
		t.Fatalf("duplicate identity admitted: %v", err)
	}
	if err := plan.Index("two", strings.Repeat("x", 100)); err == nil {
		t.Fatal("oversized action admitted")
	}
	if err := plan.Index("", true); !errors.Is(err, vectorstore.ErrMissingDocumentID) {
		t.Fatal(err)
	}
	if err := plan.Index("bad", make(chan int)); err == nil {
		t.Fatal("unencodable source admitted")
	}
	if err := plan.Delete("one", nil); err == nil {
		t.Fatal("mixed operation admitted")
	}
	deletion := bulkPlan(t, Delete, 0)
	if err := deletion.Index("one", true); err == nil {
		t.Fatal("mixed operation admitted")
	}
	for _, version := range []Version{{SeqNo: -1, PrimaryTerm: 1}, {SeqNo: 0, PrimaryTerm: 0}} {
		if err := deletion.Delete("one", &version); err == nil {
			t.Fatal("invalid version admitted")
		}
	}
	if err := (*BulkPlan)(nil).Publish(t.Context(), nil); err == nil {
		t.Fatal("nil plan accepted")
	}
	if err := new(BulkPlan).Publish(t.Context(), nil); err == nil {
		t.Fatal("uninitialized plan accepted")
	}
}

func TestBulkResponseLimitKeepsCompleteItemPrefix(t *testing.T) {
	const prefix = `{"errors":false,"items":[{"index":{"_index":"documents","_id":"one","status":201}},`
	const suffix = `{"index":{"_index":"documents","_id":"two","status":201}}]}`
	transport := &scriptedTransport{t: t, replies: []wireReply{{status: http.StatusOK, body: prefix + suffix}}}
	plan := bulkPlan(t, Index, 0)
	for _, id := range []string{"one", "two"} {
		if err := plan.Index(id, true); err != nil {
			t.Fatal(err)
		}
	}
	err := plan.Publish(t.Context(), protocolClient(t, transport, int64(len(prefix))))
	writeErr, ok := errors.AsType[*vectorstore.WriteError](err)
	if !ok || !errors.Is(err, ErrResponseLimit) || !slices.Equal(writeErr.Outcome().Acknowledged, []string{"one"}) || !slices.Equal(writeErr.Outcome().Unconfirmed, []string{"two"}) {
		t.Fatalf("bounded receipt: %v", err)
	}
}
