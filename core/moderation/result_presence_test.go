package moderation_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/moderation"
)

func TestResponseJSONRequiresCompleteVerdicts(t *testing.T) {
	for _, verdict := range []string{
		`null`, `{}`, `{"flagged":false}`, `{"score":0.99}`,
		`{"flagged":null,"score":0.99}`, `{"flagged":false,"score":null}`,
	} {
		t.Run(verdict, func(t *testing.T) {
			kept := &moderation.Output{Categories: moderation.Categories{"violence": {Flagged: true, Score: 0.8}}}
			response := moderation.Response{Outputs: []*moderation.Output{kept}}
			data := `{"outputs":[{"categories":{"violence":` + verdict + `}}]}`
			if err := jsonv2.Unmarshal([]byte(data), &response); !errors.Is(err, moderation.ErrInvalidResponse) {
				t.Fatalf("decode error = %v, want ErrInvalidResponse", err)
			}
			if response.First() != kept || kept.Categories["violence"] != (moderation.Verdict{Flagged: true, Score: 0.8}) {
				t.Fatal("rejected verdict changed the retained response")
			}
		})
	}
}

func TestVerdictJSONPreservesExplicitUnflaggedResults(t *testing.T) {
	for _, score := range []string{`0`, `0.99`} {
		data := `{"flagged":false,"score":` + score + `}`
		var verdict moderation.Verdict
		if err := jsonv2.Unmarshal([]byte(data), &verdict); err != nil {
			t.Fatal(err)
		}
		if verdict.Flagged || (moderation.Categories{"violence": verdict}).Flagged() {
			t.Fatal("the reported score overrode the reported flag")
		}
		encoded, err := jsonv2.Marshal(verdict)
		if err != nil || string(encoded) != data {
			t.Fatalf("round trip = %s, %v; want %s", encoded, err, data)
		}
	}
}
