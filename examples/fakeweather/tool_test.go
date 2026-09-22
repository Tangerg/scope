package fakeweather

import (
	jsonv2 "encoding/json/v2"
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
