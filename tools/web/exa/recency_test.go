package exa

import (
	"testing"
	"time"

	"github.com/Tangerg/scope/tools/web"
)

func TestRecencyToStart(t *testing.T) {
	now := time.Date(2026, time.March, 31, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		recency web.Recency
		want    time.Time
	}{
		{recency: web.RecencyHour, want: time.Date(2026, time.March, 31, 11, 0, 0, 0, time.UTC)},
		{recency: web.RecencyDay, want: time.Date(2026, time.March, 30, 12, 0, 0, 0, time.UTC)},
		{recency: web.RecencyWeek, want: time.Date(2026, time.March, 24, 12, 0, 0, 0, time.UTC)},
		{recency: web.RecencyMonth, want: time.Date(2026, time.March, 3, 12, 0, 0, 0, time.UTC)},
		{recency: web.RecencyYear, want: time.Date(2025, time.March, 31, 12, 0, 0, 0, time.UTC)},
		{recency: ""},
		{recency: web.Recency("decade")},
	}
	for _, test := range tests {
		if got := recencyToStart(test.recency, now); !got.Equal(test.want) {
			t.Errorf("recencyToStart(%q) = %s, want %s", test.recency, got, test.want)
		}
	}
}

func TestParseDateAcceptsOnlyRFC3339(t *testing.T) {
	parsed := parseDate("2024-03-01T10:00:00Z")
	if !parsed.Equal(time.Date(2024, time.March, 1, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("parseDate returned %s", parsed)
	}

	for name, value := range map[string]string{
		"empty":       "",
		"date only":   "2024-03-01",
		"unix epoch":  "1709287200",
		"free text":   "March 1, 2024",
		"truncated":   "2024-03-01T10:00",
		"wrong zone":  "2024-03-01T10:00:00 UTC",
		"nonsensical": "not-a-date",
	} {
		t.Run(name, func(t *testing.T) {
			if got := parseDate(value); !got.IsZero() {
				t.Fatalf("parseDate(%q) = %s, want the zero time", value, got)
			}
		})
	}
}
