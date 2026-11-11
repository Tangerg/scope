package elastic

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

type protocolBody struct {
	io.Reader
	readErr  error
	closeErr error
	closed   bool
}

func (p *protocolBody) Read(raw []byte) (int, error) {
	n, err := p.Reader.Read(raw)
	if errors.Is(err, io.EOF) && p.readErr != nil {
		return n, p.readErr
	}
	return n, err
}

func (p *protocolBody) Close() error { p.closed = true; return p.closeErr }

type wireReply struct {
	status   int
	body     string
	callErr  error
	readErr  error
	closeErr error
}

type wireRequest struct {
	method string
	path   string
	body   string
	header http.Header
}

type scriptedTransport struct {
	t        *testing.T
	replies  []wireReply
	requests []wireRequest
	bodies   []*protocolBody
}

func (s *scriptedTransport) perform(request *http.Request) (*http.Response, error) {
	s.t.Helper()
	body, err := io.ReadAll(request.Body)
	if err != nil {
		s.t.Fatal(err)
	}
	s.requests = append(s.requests, wireRequest{method: request.Method, path: request.URL.Path, body: string(body), header: request.Header.Clone()})
	if len(s.replies) == 0 {
		s.t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
	}
	reply := s.replies[0]
	s.replies = s.replies[1:]
	responseBody := &protocolBody{Reader: strings.NewReader(reply.body), readErr: reply.readErr, closeErr: reply.closeErr}
	s.bodies = append(s.bodies, responseBody)
	return &http.Response{StatusCode: reply.status, Body: responseBody}, reply.callErr
}

func protocolClient(t *testing.T, transport *scriptedTransport, limit int64) *Client {
	t.Helper()
	client, err := NewClient(ClientConfig{Perform: transport.perform, MaxResponseBytes: limit})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func bulkPlan(t *testing.T, operation Operation, budget int, allowMissing bool) *BulkPlan {
	t.Helper()
	plan, err := NewBulkPlan(BulkConfig{Index: "documents", Operation: operation, MaxBytes: budget, AllowMissing: allowMissing})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func fixtureBudget() vectorstore.ScanBudget {
	return vectorstore.ScanBudget{MaxRecords: 10, MaxBytes: 65536, Timeout: time.Second}
}

func decodeFixtureHit(hit Hit) (*document.Document, error) {
	doc := &document.Document{ID: hit.ID, Text: "text"}
	if err := doc.UnmarshalStoredMetadata(hit.Source); err != nil {
		return nil, err
	}
	return doc, nil
}
