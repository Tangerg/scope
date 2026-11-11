package opensearch

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	opensearchsdk "github.com/opensearch-project/opensearch-go/v5"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestNMSLibFilteredSearchRejectsBeforeModelOrNativeIO(t *testing.T) {
	index := newNativeIndex(t)
	index.setEmbedding(func(field *fixtureField) { field.Method.Engine = engineNMSLIB })
	store := index.store()
	if response, err := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{Filter: filter.IsNull("value")}}); !errors.Is(err, errors.ErrUnsupported) || response != nil || len(index.Groups) != 0 {
		t.Fatalf("unsupported operation reached model or native query: %#v, %v", response, err)
	}
}

type countedBody struct {
	io.Reader
	read   int
	closed bool
}

func (c *countedBody) Read(buffer []byte) (int, error) {
	n, err := c.Reader.Read(buffer)
	c.read += n
	return n, err
}

func (c *countedBody) Close() error { c.closed = true; return nil }

type nativeBodyTransport struct{ body *countedBody }

func (n nativeBodyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if response, health := nativeHealthResponse(request); health {
		encoded, err := jsonv2.Marshal(response)
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(encoded))}, nil
	}
	if request.Method != http.MethodGet || request.URL.Path != "/documents/_mapping" {
		return nil, fmt.Errorf("unexpected native request %s %s", request.Method, request.URL)
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: n.body}, nil
}

func TestNativeStreamingTransportEnforcesBoundBeforeBuffering(t *testing.T) {
	body := &countedBody{Reader: strings.NewReader(strings.Repeat("x", 1<<20))}
	client, err := opensearchsdk.NewClient(opensearchsdk.Config{Addresses: []string{"http://native.fixture"}, Transport: nativeBodyTransport{body: body}, DisableRetry: true, DiscoverNodesOnStart: new(false)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := client.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	protocol := fixtureProtocol(t, client, 16)
	if raw, requestErr := protocol.Read(t.Context(), http.MethodGet, "/documents/_mapping", nil, nil, 0); requestErr == nil || len(raw) != 17 || body.read != 17 || !body.closed {
		t.Fatalf("native transport buffered past boundary: read=%d closed=%v raw=%s error=%v", body.read, body.closed, raw, requestErr)
	}
}
