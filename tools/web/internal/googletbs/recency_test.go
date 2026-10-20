package googletbs

import (
	"testing"

	"github.com/Tangerg/scope/tools/web"
)

func TestRecencyMappingMatchesSerperTBS(t *testing.T) {
	tests := []struct {
		name    string
		recency web.Recency
		want    string
	}{
		{name: "hour", recency: web.RecencyHour, want: "qdr:h"},
		{name: "day", recency: web.RecencyDay, want: "qdr:d"},
		{name: "week", recency: web.RecencyWeek, want: "qdr:w"},
		{name: "month", recency: web.RecencyMonth, want: "qdr:m"},
		{name: "year", recency: web.RecencyYear, want: "qdr:y"},
		{name: "unset"},
		{name: "unsupported", recency: web.Recency("unsupported")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := FromRecency(test.recency); got != test.want {
				t.Fatalf("FromRecency(%q) = %q, want %q", test.recency, got, test.want)
			}
		})
	}
}
