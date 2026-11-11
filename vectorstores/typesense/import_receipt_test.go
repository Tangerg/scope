package typesense

import (
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore"
)

type receiptBody struct {
	io.Reader
	readErr  error
	closeErr error
}

func (r *receiptBody) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if errors.Is(err, io.EOF) && r.readErr != nil {
		return n, r.readErr
	}
	return n, err
}

func (r *receiptBody) Close() error { return r.closeErr }

func TestImportPreservesReceivedEvidenceWithTransportFailure(t *testing.T) {
	closeErr := errors.New("receipt close failed")
	for _, sample := range []struct {
		name         string
		body         string
		readErr      error
		closeErr     error
		status       int
		acknowledged []string
		unconfirmed  []string
	}{
		{name: "partial read", body: "{\"id\":\"b25l\",\"success\":true}\n", readErr: io.ErrUnexpectedEOF, status: http.StatusOK, acknowledged: []string{"one"}, unconfirmed: []string{"two"}},
		{name: "complete close", body: "{\"id\":\"b25l\",\"success\":true}\n{\"id\":\"dHdv\",\"success\":true}\n", closeErr: closeErr, status: http.StatusOK, acknowledged: []string{"one", "two"}},
		{name: "failed envelope", body: "{\"id\":\"b25l\",\"success\":true}\n", closeErr: closeErr, status: http.StatusBadGateway, unconfirmed: []string{"one", "two"}},
	} {
		t.Run(sample.name, func(t *testing.T) {
			body := &receiptBody{Reader: strings.NewReader(sample.body), readErr: sample.readErr, closeErr: sample.closeErr}
			raw, readErr := readNativeResponse(&http.Response{StatusCode: sample.status, Body: body}, nil, DefaultMaxResponseBytes)
			err := validateImport(raw, []string{"one", "two"}, readErr)
			writeErr, ok := errors.AsType[*vectorstore.WriteError](err)
			if !ok {
				t.Fatalf("missing write evidence: %v", err)
			}
			outcome := writeErr.Outcome()
			if !slices.Equal(outcome.Acknowledged, sample.acknowledged) || !slices.Equal(outcome.Unconfirmed, sample.unconfirmed) {
				t.Fatalf("outcome=%+v", outcome)
			}
			for _, cause := range []error{sample.readErr, sample.closeErr} {
				if cause != nil && !errors.Is(err, cause) {
					t.Fatalf("lost cause %v: %v", cause, err)
				}
			}
		})
	}
}

func TestImportResponseLimitKeepsCompleteReceiptPrefix(t *testing.T) {
	const accepted = "{\"id\":\"b25l\",\"success\":true}\n"
	body := io.NopCloser(strings.NewReader(accepted + "{\"id\":\"dHdv\",\"success\":true}\n"))
	raw, readErr := readNativeResponse(&http.Response{StatusCode: http.StatusOK, Body: body}, nil, int64(len(accepted)))
	err := validateImport(raw, []string{"one", "two"}, readErr)
	writeErr, ok := errors.AsType[*vectorstore.WriteError](err)
	if !ok || !errors.Is(err, errResponseLimit) || !slices.Equal(writeErr.Outcome().Acknowledged, []string{"one"}) || !slices.Equal(writeErr.Outcome().Unconfirmed, []string{"two"}) {
		t.Fatalf("bounded receipt: %v", err)
	}
}
