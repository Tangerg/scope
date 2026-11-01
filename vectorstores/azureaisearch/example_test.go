package azureaisearch_test

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/azureaisearch"
)

type exampleAuthentication struct {
	key  string
	base http.RoundTripper
}

func (e exampleAuthentication) RoundTrip(request *http.Request) (*http.Response, error) {
	authenticated := request.Clone(request.Context())
	authenticated.Header.Set("api-key", e.key)
	return e.base.RoundTrip(authenticated)
}

func ExampleNewStore() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport:     exampleAuthentication{key: os.Getenv("AZURE_SEARCH_API_KEY"), base: transport},
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	store, err := azureaisearch.NewStore(ctx, azureaisearch.StoreConfig{
		Endpoint: "https://example.search.windows.net", IndexName: "documents", HTTPClient: client,
		Space: "manual-v1",
	})
	if err != nil {
		panic(err)
	}
	if _, err = store.Query(ctx, &vectorstore.Query{Space: "manual-v1", Values: []float64{1, 0}, LexicalText: "example", Options: vectorstore.SearchOptions{Mode: vectorstore.SearchModeHybrid}}); err != nil {
		panic(err)
	}
	if err = store.DeleteIDs(ctx, []string{"document-key"}); err != nil {
		panic(err)
	}
}
