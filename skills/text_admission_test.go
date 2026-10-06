package skills_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/skills"
)

func TestSkillParserRejectsUnencodableInstructions(t *testing.T) {
	skill, err := skills.Parse([]byte("---\nname: example\ndescription: example\n---\n\xff"))
	if !errors.Is(err, skills.ErrInvalidSkill) || skill != nil {
		t.Fatalf("Parse() = %+v, %v", skill, err)
	}
}

func TestSkillRejectsUnencodableMetadata(t *testing.T) {
	for _, mutate := range []func(*skills.Frontmatter){
		func(f *skills.Frontmatter) { f.Description = "\xff" },
		func(f *skills.Frontmatter) { f.License = "\xff" },
		func(f *skills.Frontmatter) { f.Compatibility = "\xff" },
		func(f *skills.Frontmatter) { f.AllowedTools = "\xff" },
		func(f *skills.Frontmatter) { f.Metadata = map[string]string{"\xff": "valid"} },
		func(f *skills.Frontmatter) { f.Metadata = map[string]string{"valid": "\xff"} },
	} {
		frontmatter := skills.Frontmatter{Name: "example", Description: "example"}
		mutate(&frontmatter)
		if validationErr := frontmatter.Validate(); !errors.Is(validationErr, skills.ErrInvalidSkill) {
			t.Fatalf("Validate(%+v) = %v", frontmatter, validationErr)
		}
	}
	if validationErr := (skills.Summary{Name: "example", Description: "\xff"}).Validate(); !errors.Is(validationErr, skills.ErrInvalidSkill) {
		t.Fatalf("Summary.Validate() = %v", validationErr)
	}
}

func TestSkillInstructionsRemainExact(t *testing.T) {
	body := "\n说明\x00é\n"
	skill, err := skills.Parse([]byte("---\nname: example\ndescription: example\n---\n" + body))
	if err != nil {
		t.Fatal(err)
	}
	if skill.Instructions != body {
		t.Fatalf("body changed: %q", skill.Instructions)
	}
	encoded, err := jsonv2.Marshal(skill)
	if err != nil {
		t.Fatal(err)
	}
	var decoded skills.Skill
	if decodeErr := jsonv2.Unmarshal(encoded, &decoded); decodeErr != nil || decoded.Instructions != body {
		t.Fatalf("round trip = %q, %v", decoded.Instructions, decodeErr)
	}
}
