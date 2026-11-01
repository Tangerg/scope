package bedrock

import (
	"errors"
	"io"
	"net/http"
	"testing"
)

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
