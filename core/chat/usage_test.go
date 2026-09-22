package chat_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/chat"
)

func TestUsageZeroAndRoundTrip(t *testing.T) {
	var zero chat.Usage
	if err := zero.Validate(); err != nil || zero.TotalTokens() != 0 {
		t.Fatalf("zero Usage = (%d, %v)", zero.TotalTokens(), err)
	}

	reasoning := int64(4)
	cacheRead := int64(3)
	cacheWrite := int64(2)
	usage := chat.Usage{
		InputTokens:           10,
		OutputTokens:          6,
		ReasoningTokens:       &reasoning,
		CacheReadInputTokens:  &cacheRead,
		CacheWriteInputTokens: &cacheWrite,
	}
	if err := usage.Validate(); err != nil || usage.TotalTokens() != 16 {
		t.Fatalf("Usage = (%d, %v)", usage.TotalTokens(), err)
	}
	encoded, err := jsonv2.Marshal(usage)
	if err != nil {
		t.Fatal(err)
	}
	var got chat.Usage
	if err := jsonv2.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, usage) {
		t.Fatalf("round trip = %#v, want %#v", got, usage)
	}
}

func TestUsageValidateRejectsInvalidValues(t *testing.T) {
	negative := int64(-1)
	tooLarge := int64(6)
	tests := []chat.Usage{
		{InputTokens: -1},
		{OutputTokens: -1},
		{InputTokens: 1<<63 - 1, OutputTokens: 1},
		{OutputTokens: 5, ReasoningTokens: &negative},
		{OutputTokens: 5, ReasoningTokens: &tooLarge},
		{InputTokens: 5, CacheReadInputTokens: &tooLarge},
		{InputTokens: 5, CacheWriteInputTokens: &tooLarge},
	}
	for _, usage := range tests {
		if err := usage.Validate(); !errors.Is(err, chat.ErrInvalidUsage) {
			t.Errorf("Validate(%+v) error = %v", usage, err)
		}
		if _, err := jsonv2.Marshal(usage); !errors.Is(err, chat.ErrInvalidUsage) {
			t.Errorf("Marshal(%+v) error = %v", usage, err)
		}
	}
}

func TestUsageUnmarshalIsAtomic(t *testing.T) {
	usage := chat.Usage{InputTokens: 10}
	if err := jsonv2.Unmarshal([]byte(`{"input_tokens":-1}`), &usage); !errors.Is(err, chat.ErrInvalidUsage) {
		t.Fatalf("Unmarshal error = %v", err)
	}
	if usage.InputTokens != 10 {
		t.Fatalf("failed Unmarshal mutated usage: %+v", usage)
	}
	var nilUsage *chat.Usage
	if err := nilUsage.UnmarshalJSON([]byte(`{}`)); !errors.Is(err, chat.ErrInvalidUsage) {
		t.Fatalf("nil UnmarshalJSON error = %v", err)
	}
}
