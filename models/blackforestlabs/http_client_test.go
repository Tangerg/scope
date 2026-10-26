package blackforestlabs

import (
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/modeltest"
)

func TestConstructionBorrowsHTTPConfiguration(t *testing.T) {
	modeltest.RunHTTPClientContract(t, modeltest.HTTPClientContract{
		Build: func(t *testing.T, baseURL string, borrowed *http.Client) []*http.Client {
			t.Helper()
			adapter, err := newAPI(t.Context(), apiConfig{APIKey: "test", BaseURL: baseURL, HTTPClient: borrowed})
			if err != nil {
				t.Fatal(err)
			}
			return []*http.Client{adapter.http.GetClient()}
		},
	})
}
