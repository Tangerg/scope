package httpclient_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/httpclient"
)

func TestResponseBudgetBoundsEveryResponseTogether(t *testing.T) {
	if _, err := httpclient.NewResponseBudget(0); err == nil {
		t.Fatal("an empty budget was granted")
	}
	budget, err := httpclient.NewResponseBudget(12)
	if err != nil {
		t.Fatal(err)
	}
	page := func(body string, status int) func(*http.Request) (*http.Response, error) {
		return func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
		}
	}
	response, err := budget.Apply(&http.Request{}, page(`{"page":1}`, http.StatusOK))
	if err != nil {
		t.Fatal(err)
	}
	if content, _ := io.ReadAll(response.Body); string(content) != `{"page":1}` {
		t.Fatalf("body = %q", content)
	}
	if _, err := budget.Apply(&http.Request{}, page(`{"p":2}`, http.StatusOK)); err == nil {
		t.Fatal("a second page exceeded the shared budget")
	}
	fresh, err := httpclient.NewResponseBudget(64)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.Apply(&http.Request{}, page(`{`, http.StatusOK)); err == nil {
		t.Fatal("a truncated successful body was admitted")
	}
	if response, err := fresh.Apply(&http.Request{}, page(`not json`, http.StatusBadGateway)); err != nil || response.StatusCode != http.StatusBadGateway {
		t.Fatalf("an error envelope lost its status: %v", err)
	}
}
