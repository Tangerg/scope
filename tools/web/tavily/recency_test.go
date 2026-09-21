package tavily

import (
	"errors"
	"testing"

	"github.com/Tangerg/scope/tools/web"
)

func TestRecencyMappingMatchesTavilyTimeRange(t *testing.T) {
	tests := []struct {
		name    string
		recency web.Recency
		want    string
	}{
		{name: "day", recency: web.RecencyDay, want: "day"},
		{name: "week", recency: web.RecencyWeek, want: "week"},
		{name: "month", recency: web.RecencyMonth, want: "month"},
		{name: "year", recency: web.RecencyYear, want: "year"},
		{name: "unset"},
		{name: "unsupported", recency: web.Recency("unsupported")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := recencyToTimeRange(test.recency); got != test.want {
				t.Fatalf("recencyToTimeRange(%q) = %q, want %q", test.recency, got, test.want)
			}
		})
	}
}

func TestSearchRejectsUnsupportedHourBeforeIO(t *testing.T) {
	client, err := NewClient(Config{APIKey: "test-key", BaseURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Search(t.Context(), &web.SearchRequest{Query: "scope", Recency: web.RecencyHour})
	if !errors.Is(err, web.ErrUnsupportedFilter) {
		t.Fatalf("Search error=%v", err)
	}
}
