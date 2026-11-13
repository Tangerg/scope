package brave

import (
	"errors"
	"testing"

	toolcontract "github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/tools/web"
)

func TestRecencyMappingMatchesBraveFreshness(t *testing.T) {
	tests := []struct {
		name    string
		recency web.Recency
		want    string
		ok      bool
	}{
		{name: "day", recency: web.RecencyDay, want: "pd", ok: true},
		{name: "week", recency: web.RecencyWeek, want: "pw", ok: true},
		{name: "month", recency: web.RecencyMonth, want: "pm", ok: true},
		{name: "year", recency: web.RecencyYear, want: "py", ok: true},
		{name: "unset", ok: true},
		{name: "hour", recency: web.RecencyHour},
		{name: "unsupported", recency: web.Recency("unsupported")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got, ok := recencyFreshness(test.recency); got != test.want || ok != test.ok {
				t.Fatalf("recencyFreshness(%q) = %q, %t; want %q, %t", test.recency, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestSearchRejectsUnsupportedHourBeforeIO(t *testing.T) {
	client, err := NewClient(t.Context(), Config{APIKey: "test-key", BaseURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Search(t.Context(), &web.SearchRequest{Query: "scope", Recency: web.RecencyHour})
	failure, definite := toolcontract.ClassifyFailure(err)
	if !definite || failure.Kind() != toolcontract.FailureKindFailed || !errors.Is(failure.Cause(), web.ErrUnsupportedFilter) {
		t.Fatalf("Search error=%v", err)
	}
}
