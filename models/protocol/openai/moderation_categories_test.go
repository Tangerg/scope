package openai

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	openaisdk "github.com/openai/openai-go/v3"

	"github.com/Tangerg/scope/core/moderation"
)

func TestModerationRequiresReportedCategoryFacts(t *testing.T) {
	for _, result := range []string{
		`{}`,
		`{"categories":{"self-harm":true,"self_harm":false},"category_scores":{"self-harm":0.9,"self_harm":0}}`,
		`{"categories":{"hate":false}}`,
		`{"category_scores":{"hate":0}}`,
		`{"categories":{"hate":null},"category_scores":{"hate":0}}`,
		`{"categories":{"hate":false},"category_scores":{"hate":null}}`,
		`{"categories":{"hate":false},"category_scores":{"violence":0}}`,
	} {
		t.Run(result, func(t *testing.T) {
			var native openaisdk.ModerationNewResponse
			if err := jsonv2.Unmarshal([]byte(`{"results":[`+result+`]}`), &native); err != nil {
				t.Fatal(err)
			}
			model := &ModerationModel{}
			if response, err := model.buildModerationResponse(&native); !errors.Is(err, moderation.ErrInvalidResponse) {
				t.Fatalf("response=%v error=%v", response, err)
			}
		})
	}
}

func TestModerationPreservesReportedCategorySet(t *testing.T) {
	var native openaisdk.ModerationNewResponse
	raw := `{"results":[{"categories":{"hate":false,"future/category":true},"category_scores":{"hate":0,"future/category":0.75}}]}`
	if err := jsonv2.Unmarshal([]byte(raw), &native); err != nil {
		t.Fatal(err)
	}
	model := &ModerationModel{}
	response, err := model.buildModerationResponse(&native)
	if err != nil {
		t.Fatal(err)
	}
	categories := response.Outputs[0].Categories
	if len(categories) != 2 || categories["hate"] != (moderation.Verdict{Flagged: false, Score: 0}) || categories["future_category"] != (moderation.Verdict{Flagged: true, Score: 0.75}) {
		t.Fatalf("categories=%v", categories)
	}
}
