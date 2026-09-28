package skills_test

import (
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Tangerg/scope/skills"
)

func TestLookupMeasuresRawFrontmatterBytes(t *testing.T) {
	const metadata = "---\nname: audit\ndescription: inspect\n---"
	for _, test := range []struct {
		name    string
		content string
	}{
		{"no final newline", metadata},
		{"lf", metadata + "\n"},
		{"bom lf", "\ufeff" + metadata + "\n"},
		{"crlf", strings.ReplaceAll(metadata+"\n", "\n", "\r\n")},
		{"bom crlf", "\ufeff" + strings.ReplaceAll(metadata+"\n", "\n", "\r\n")},
	} {
		for _, budget := range []struct {
			name  string
			extra int64
		}{{"exact", 0}, {"over", -1}} {
			t.Run(test.name+"/"+budget.name, func(t *testing.T) {
				repository, err := skills.NewRepository(fstest.MapFS{
					"audit/SKILL.md": &fstest.MapFile{Data: []byte(test.content)},
				}, skills.RepositoryConfig{MaxFrontmatterBytes: int64(len(test.content)) + budget.extra})
				if err != nil {
					t.Fatal(err)
				}
				summary, err := repository.Lookup(t.Context(), "audit")
				if budget.extra < 0 {
					if !errors.Is(err, skills.ErrContentTooLarge) {
						t.Fatalf("Lookup error = %v, want ErrContentTooLarge", err)
					}
					return
				}
				if err != nil || summary != (skills.Summary{Name: "audit", Description: "inspect"}) {
					t.Fatalf("Lookup at exact byte limit = %#v, %v", summary, err)
				}
			})
		}
	}
}

func TestLookupRejectsTruncatedClosingFence(t *testing.T) {
	const prefix = "\ufeff---\nname: audit\ndescription: inspect\n"
	repository, err := skills.NewRepository(fstest.MapFS{
		"audit/SKILL.md": &fstest.MapFile{Data: []byte(prefix + "----\ninstructions")},
	}, skills.RepositoryConfig{MaxFrontmatterBytes: int64(len(prefix) + 2)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Lookup(t.Context(), "audit"); !errors.Is(err, skills.ErrContentTooLarge) {
		t.Fatalf("Lookup error = %v, want ErrContentTooLarge", err)
	}
}
