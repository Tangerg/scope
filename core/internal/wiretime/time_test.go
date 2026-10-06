package wiretime_test

import (
	jsonv2 "encoding/json/v2"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/internal/wiretime"
)

func TestValidatedTimestampPreservesItsInstant(t *testing.T) {
	tests := []struct {
		name  string
		value time.Time
		valid bool
	}{
		{"omitted", time.Time{}, true},
		{"omitted with offset", time.Time{}.In(time.FixedZone("seconds", 43)), true},
		{"minimum year", time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC), true},
		{"maximum year", time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC), true},
		{"positive offset", time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.FixedZone("positive", (23*60+59)*60)), true},
		{"negative offset", time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.FixedZone("negative", -(23*60+59)*60)), true},
		{"negative year", time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC), false},
		{"overflow year", time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), false},
		{"overflow offset", time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("overflow", 24*60*60)), false},
		{"positive seconds", time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.FixedZone("seconds", 43)), false},
		{"negative seconds", time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.FixedZone("seconds", -43)), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := wiretime.Validate(test.value); (err == nil) != test.valid {
				t.Fatalf("Validate() = %v; want valid=%v", err, test.valid)
			}
			if !test.valid {
				return
			}
			wire := struct {
				Value time.Time `json:"value,omitzero"`
			}{Value: test.value}
			encoded, err := jsonv2.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			var decoded struct {
				Value time.Time `json:"value,omitzero"`
			}
			if decodeErr := jsonv2.Unmarshal(encoded, &decoded); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if !test.value.Equal(decoded.Value) {
				t.Fatalf("timestamp changed during JSON round trip: %v -> %v", test.value, decoded.Value)
			}
		})
	}
}
