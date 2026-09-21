package blackforestlabs

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRedirectCredentialBoundary(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(map[bool]string{false: "same origin", true: "foreign origin"}[foreign], func(t *testing.T) {
			received := 0
			target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				received++
				t.Errorf("foreign server received x-key %q", request.Header.Get("x-key"))
			}))
			defer target.Close()
			sameOrigin := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/finish" {
					sameOrigin++
					if request.Header.Get("x-key") != "sentinel" {
						t.Error("missing provider credential")
					}
					writer.Header().Set("Content-Type", "application/json")
					_, _ = writer.Write([]byte(`{"id":"task"}`))
					return
				}
				location := "/finish"
				if foreign {
					location = target.URL
				}
				http.Redirect(writer, request, location, http.StatusTemporaryRedirect)
			}))
			defer server.Close()
			client := server.Client()
			adapter, err := newAPI(apiConfig{APIKey: "sentinel", BaseURL: server.URL, HTTPClient: client})
			if err != nil {
				t.Fatal(err)
			}
			_, err = adapter.generate(t.Context(), "generate", &generateRequest{Prompt: "test"})
			if foreign && err == nil {
				t.Fatal("foreign redirect accepted")
			}
			if !foreign && (err != nil || sameOrigin != 1) {
				t.Fatalf("same-origin redirect: %d, %v", sameOrigin, err)
			}
			if received != 0 {
				t.Fatalf("foreign requests = %d", received)
			}
			if client.CheckRedirect != nil {
				t.Fatal("caller client was mutated")
			}
		})
	}
}
