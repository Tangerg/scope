package ranking_test

import (
	"errors"
	"testing"

	"github.com/Tangerg/scope/eval/ranking"
)

func TestRankingRejectsUnencodableCorrelationIdentities(t *testing.T) {
	for _, sample := range []ranking.Sample{
		{Ranking: []string{"\xff"}, Judgments: []ranking.Judgment{{Identity: "valid", Relevance: 1}}},
		{Ranking: []string{"valid"}, Judgments: []ranking.Judgment{{Identity: "\xff", Relevance: 1}}},
	} {
		if validationErr := sample.Validate(); !errors.Is(validationErr, ranking.ErrInvalidSample) {
			t.Fatalf("Validate() = %v", validationErr)
		}
		if _, err := ranking.NewSample(sample.Ranking, sample.Judgments); !errors.Is(err, ranking.ErrInvalidSample) {
			t.Fatalf("NewSample() = %v", err)
		}
	}
	if _, err := ranking.NewSample([]string{"文档\x00é"}, []ranking.Judgment{{Identity: "文档\x00é", Relevance: 1}}); err != nil {
		t.Fatal(err)
	}
}
