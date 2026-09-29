package fakeweather

import (
	"testing"
	"time"
)

func TestSummerNotFreezing(t *testing.T) {
	locations := []string{
		"Beijing", "London", "Tokyo", "New York", "Paris",
		"atlantis", "vegapunk", "shangri-la", "the moon", "valhalla",
		"Foo City", "Unknown Town",
	}
	dates := []string{"2024-06-15", "2024-07-15", "2024-08-15"}

	for _, loc := range locations {
		for _, d := range dates {
			resp, err := generate(&Request{Location: loc, Date: d})
			if err != nil {
				t.Fatalf("generate(%q, %q): %v", loc, d, err)
			}
			if resp.Temperature.Value < 5 {
				t.Errorf("northern-hemisphere summer for %q on %s produced Temperature.Value=%d (< 5°C); expected at least mild",
					loc, d, resp.Temperature.Value)
			}
			if resp.Temperature.Min < 0 {
				t.Errorf("northern-hemisphere summer for %q on %s produced Temperature.Min=%d (< 0°C)",
					loc, d, resp.Temperature.Min)
			}
		}
	}
}

func TestKnownSouthernCityFlipsSeasons(t *testing.T) {
	resp, err := generate(&Request{Location: "Sao Paulo", Date: "2024-07-15"})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if resp.Temperature.Value > 25 {
		t.Errorf("Sao Paulo in July (southern winter) produced Temperature.Value=%d, expected < 25°C", resp.Temperature.Value)
	}

	resp, err = generate(&Request{Location: "Sao Paulo", Date: "2024-01-15"})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if resp.Temperature.Value < 18 {
		t.Errorf("Sao Paulo in January (southern summer) produced Temperature.Value=%d, expected ≥ 18°C", resp.Temperature.Value)
	}
}

func TestTyphoonAlertsFollowTheLocalSeason(t *testing.T) {
	southernWinter := time.Date(2024, time.June, 1, 0, 0, 0, 0, time.UTC)
	for _, location := range []string{"Jakarta", "Sydney", "Brisbane", "Sao Paulo", "Rio de Janeiro", "Buenos Aires"} {
		for day := southernWinter; day.Month() <= time.October; day = day.AddDate(0, 0, 1) {
			resp, err := generate(&Request{Location: location, Date: day.Format(time.DateOnly)})
			if err != nil {
				t.Fatal(err)
			}
			for _, alert := range resp.Alerts {
				if alert.Type == AlertTyphoon {
					t.Fatalf("%s on %s reported a typhoon outside its local typhoon season", location, day.Format(time.DateOnly))
				}
			}
		}
	}
}

func TestDeterministic(t *testing.T) {
	req := &Request{Location: "Beijing", Date: "2024-07-15", IncludeAirQuality: true, IncludeHourly: true}
	a, err := generate(req)
	if err != nil {
		t.Fatalf("generate a: %v", err)
	}
	b, err := generate(req)
	if err != nil {
		t.Fatalf("generate b: %v", err)
	}
	if a.Temperature != b.Temperature {
		t.Errorf("Temperature not deterministic:\n  a=%+v\n  b=%+v", a.Temperature, b.Temperature)
	}
	if a.Wind != b.Wind {
		t.Errorf("Wind not deterministic")
	}
	if a.LastUpdated != b.LastUpdated {
		t.Errorf("LastUpdated not deterministic: a=%d b=%d", a.LastUpdated, b.LastUpdated)
	}
	if len(a.HourlyForecast) != len(b.HourlyForecast) {
		t.Fatalf("HourlyForecast length mismatch: %d vs %d", len(a.HourlyForecast), len(b.HourlyForecast))
	}
	for i := range a.HourlyForecast {
		if a.HourlyForecast[i] != b.HourlyForecast[i] {
			t.Errorf("HourlyForecast[%d] not deterministic", i)
		}
	}
}

func TestTemperatureBounds(t *testing.T) {
	cases := []struct {
		location string
		zone     climateZone
	}{
		{"Beijing", zoneContinental},
		{"Tokyo", zoneSubtropical},
		{"Singapore", zoneTropical},
		{"Dubai", zoneDesert},
		{"London", zoneOceanic},
		{"Reykjavik", zonePolar},
		{"Geneva", zoneAlpine},
		{"Rome", zoneMediterranean},
	}
	dates := []string{"2024-01-15", "2024-04-15", "2024-07-15", "2024-10-15"}

	for _, tc := range cases {
		profile := climateProfiles[tc.zone]
		for _, d := range dates {
			resp, err := generate(&Request{Location: tc.location, Date: d})
			if err != nil {
				t.Fatalf("generate(%q, %q): %v", tc.location, d, err)
			}
			if resp.Temperature.Value < profile.floor || resp.Temperature.Value > profile.ceiling {
				t.Errorf("%s on %s: Value=%d outside [%d, %d]",
					tc.location, d, resp.Temperature.Value, profile.floor, profile.ceiling)
			}
			if resp.Temperature.Min > resp.Temperature.Value || resp.Temperature.Max < resp.Temperature.Value {
				t.Errorf("%s on %s: Min=%d Value=%d Max=%d (Min/Max must bracket Value)",
					tc.location, d, resp.Temperature.Min, resp.Temperature.Value, resp.Temperature.Max)
			}
		}
	}
}

func TestEmptyDateUsesToday(t *testing.T) {
	resp, err := generate(&Request{Location: "Beijing", Date: ""})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if resp.LastUpdated == 0 {
		t.Error("LastUpdated zero for empty Date")
	}
	if resp.Timestamp.Start >= resp.Timestamp.End {
		t.Errorf("Timestamp window invalid: %d → %d", resp.Timestamp.Start, resp.Timestamp.End)
	}
}

func TestInvalidDateRejected(t *testing.T) {
	_, err := generate(&Request{Location: "Beijing", Date: "yesterday"})
	if err == nil {
		t.Fatal("expected error for malformed date")
	}
}

func TestKnownCitiesAllResolve(t *testing.T) {
	for name, profile := range knownCities {
		zone := identifyClimateZone(name)
		if zone != profile.Zone {
			t.Errorf("identifyClimateZone(%q) = %d, want %d (per cities.go)", name, zone, profile.Zone)
		}
		if _, ok := climateProfiles[zone]; !ok {
			t.Errorf("city %q maps to zone %d which has no climateProfile", name, zone)
		}
	}
}

func TestEveryClimateZoneHasConditions(t *testing.T) {
	for zone := zoneTemperate; zone <= zoneAlpine; zone++ {
		for month := 1; month <= 12; month++ {
			for temp := -60; temp <= 55; temp++ {
				if len(zone.candidateConditions(temp, month)) == 0 {
					t.Fatalf("zone %d has no conditions for %d°C in month %d", zone, temp, month)
				}
			}
		}
	}
}

func TestRegionalAliases(t *testing.T) {
	cases := []struct {
		query string
		want  climateZone
	}{
		{"crossing the sahara", zoneDesert},
		{"gobi desert expedition", zoneDesert},
		{"antarctica research base", zonePolar},
		{"alaska wilderness", zonePolar},
	}
	for _, tc := range cases {
		if got := identifyClimateZone(tc.query); got != tc.want {
			t.Errorf("identifyClimateZone(%q) = %d, want %d", tc.query, got, tc.want)
		}
	}
}
