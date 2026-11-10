package bedrock

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/httpclient"
)

func TestRequestBodyClientLimitsJSONWithoutLimitingSuccessfulEventStreams(t *testing.T) {
	const limit = httpclient.DefaultMaxJSONResponseBytes
	for _, test := range []struct {
		path   string
		status int
		limit  bool
	}{
		{"/model/test/invoke", http.StatusOK, true},
		{"/model/test/converse-stream", http.StatusBadRequest, true},
		{"/model/test/converse-stream", http.StatusOK, false},
	} {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://bedrock.example.test"+test.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		client := requestBodyClient{client: oversizedResponseClient{status: test.status}}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		n, readErr := io.Copy(io.Discard, response.Body)
		closeErr := response.Body.Close()
		_, limited := errors.AsType[*http.MaxBytesError](readErr)
		wantBytes := int64(limit)
		if !test.limit {
			wantBytes++
		}
		if closeErr != nil || limited != test.limit || n != wantBytes || !test.limit && readErr != nil {
			t.Fatalf("path=%s status=%d bytes=%d read=%v close=%v", test.path, test.status, n, readErr, closeErr)
		}
	}
}

type oversizedResponseClient struct{ status int }

func (o oversizedResponseClient) Do(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: o.status, Body: io.NopCloser(strings.NewReader(strings.Repeat(" ", httpclient.DefaultMaxJSONResponseBytes+1)))}, nil
}

func TestRequestBodyClientPreservesEmptyRequests(t *testing.T) {
	for _, test := range []struct {
		name string
		body io.ReadCloser
	}{{"nil", nil}, {"NoBody", http.NoBody}} {
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://bedrock.example.test", nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Body = test.body
			client := requestBodyClient{client: emptyRequestClient{body: test.body}}
			response, err := client.Do(request)
			if err != nil || response == nil || response.StatusCode != http.StatusNoContent {
				t.Fatalf("empty request response = %#v, %v", response, err)
			}
		})
	}
}

type emptyRequestClient struct {
	body io.ReadCloser
}

func (e emptyRequestClient) Do(request *http.Request) (*http.Response, error) {
	if request.Body != e.body || request.ContentLength != 0 {
		return nil, errors.New("empty request lost its body or length semantics")
	}
	return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
}
