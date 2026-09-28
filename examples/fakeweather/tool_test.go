package fakeweather

import (
	jsonv2 "encoding/json/v2"
	"slices"
	"testing"
)

func TestToolUsesOnePreciseContract(t *testing.T) {
	tool := New(nil)
	if got := tool.Definition().Name; got != "get_synthetic_weather" {
		t.Fatalf("tool name = %q, want get_synthetic_weather", got)
	}
	for _, arguments := range []string{
		`{}`,
		`{"location":"   "}`,
		`{"location":"Beijing","date":"tomorrow"}`,
		`{"location":"Beijing","unknown":true}`,
		`{"location":"Beijing"} {}`,
	} {
		if _, err := invokeTestTool(t.Context(), tool, arguments); err == nil {
			t.Fatalf("synthetic weather accepted arguments outside its contract: %s", arguments)
		}
	}

	output, err := invokeTestTool(t.Context(), tool, `{"location":"Beijing","date":"2026-08-04"}`)
	if err != nil {
		t.Fatalf("Call(valid): %v", err)
	}
	var response Response
	if err := jsonv2.Unmarshal(output.Details, &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Location != "Beijing" {
		t.Fatalf("response location = %q, want Beijing", response.Location)
	}
}

func TestToolLocationSelectionIsDeterministic(t *testing.T) {
	request := Request{
		Location: "London via Beijing and Cairo", Date: "2024-07-15",
		IncludeAirQuality: true, IncludeHourly: true,
	}
	var previous []byte
	for range 32 {
		response := callWeatherTool(t, request)
		if response.Coordinates != (Coordinates{Latitude: 51.5074, Longitude: -0.1278, Elevation: 11}) {
			t.Fatalf("coordinates = %+v, want the first location, London", response.Coordinates)
		}
		encoded, err := jsonv2.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		if previous != nil && !slices.Equal(previous, encoded) {
			t.Fatal("the same request produced different weather reports")
		}
		previous = encoded
	}
}

func TestToolRegionSelectionIsDeterministic(t *testing.T) {
	for range 32 {
		response := callWeatherTool(t, Request{Location: "Sahara via Antarctica", Date: "2024-07-15"})
		if response.Temperature.Value < 30 {
			t.Fatalf("first region is Sahara, got polar temperature %d", response.Temperature.Value)
		}
	}
}

func TestToolHourlyTemperaturePeaksInAfternoon(t *testing.T) {
	response := callWeatherTool(t, Request{Location: "London", Date: "2024-07-15", IncludeHourly: true})
	if len(response.HourlyForecast) != 24 {
		t.Fatalf("hourly forecast length = %d, want 24", len(response.HourlyForecast))
	}
	afternoon := response.HourlyForecast[14].Temperature
	night := response.HourlyForecast[2].Temperature
	if afternoon-night < 8 {
		t.Fatalf("14:00 = %d and 02:00 = %d, want the daily temperature swing from night to afternoon", afternoon, night)
	}
}

func TestToolSouthernSummerHourlyConditions(t *testing.T) {
	response := callWeatherTool(t, Request{Location: "Cape Town", Date: "2024-01-15", IncludeHourly: true})
	summerConditions := []Condition{ConditionSunny, ConditionClear, ConditionHot, ConditionPartlyCloudy}
	for i, hour := range response.HourlyForecast {
		if !slices.Contains(summerConditions, hour.Condition) {
			t.Fatalf("southern summer hour %d has winter condition %q", i, hour.Condition)
		}
	}
}

func callWeatherTool(t *testing.T, request Request) Response {
	t.Helper()
	arguments, err := jsonv2.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	output, err := invokeTestTool(t.Context(), New(nil), string(arguments))
	if err != nil {
		t.Fatal(err)
	}
	var response Response
	if err := jsonv2.Unmarshal(output.Details, &response); err != nil {
		t.Fatal(err)
	}
	return response
}
