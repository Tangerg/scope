package filter_test

import (
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestIdentifierPathRefusesWhatQueryTextCannotName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		source  string
		want    []string
		wantErr string
	}{
		{name: "bare identifier", source: `author == 'x'`, want: []string{"author"}},
		{name: "underscored", source: `author_2 == 'x'`, want: []string{"author_2"}},
		{
			name:   "indexed identifier",
			source: `profile['author'] == 'x'`,
			want:   []string{"profile", "author"},
		},
		{
			name:    "operator injection",
			source:  `profile['a:1 OR b'] == 'x'`,
			wantErr: "a:1 OR b",
		},
		{name: "spaces", source: `profile['a b'] == 'x'`, wantErr: "a b"},
		{name: "quote", source: `profile['a"b'] == 'x'`, wantErr: `a\"b`},
		{name: "brace", source: `profile['a}|@b'] == 'x'`, wantErr: "a}|@b"},
		{name: "dot", source: `profile['a.b'] == 'x'`, wantErr: "a.b"},
		{name: "hyphen", source: `profile['a-b'] == 'x'`, wantErr: "a-b"},
		{name: "empty", source: `profile[''] == 'x'`, wantErr: `""`},
		{name: "leading digit", source: `profile['1a'] == 'x'`, wantErr: "1a"},
		{name: "nested injection", source: `profile['a']['b c'] == 'x'`, wantErr: "b c"},
		{name: "array index", source: `tags[0] == 'x'`, wantErr: "array index [0]"},
		{name: "nested array index", source: `profile['a'][3] == 'x'`, wantErr: "array index [3]"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			expression, err := filter.Parse(test.source)
			if err != nil {
				t.Fatalf("Parse(%q) = %v", test.source, err)
			}
			binary, ok := expression.(*filter.BinaryExpr)
			if !ok {
				t.Fatalf("Parse(%q) = %T, want *filter.BinaryExpr", test.source, expression)
			}

			keys, err := binary.IdentifierPath()
			if test.wantErr != "" {
				if err == nil {
					t.Fatalf("IdentifierPath() = %v, want a refusal naming %s", keys, test.wantErr)
				}
				if !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("IdentifierPath() = %v, want the error to name %s", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("IdentifierPath() = %v, want nil", err)
			}
			if len(keys) != len(test.want) {
				t.Fatalf("IdentifierPath() = %v, want %v", keys, test.want)
			}
			for index, want := range test.want {
				if keys[index] != want {
					t.Fatalf("IdentifierPath()[%d] = %q, want %q", index, keys[index], want)
				}
			}
		})
	}
}

func TestPathStaysUnrestricted(t *testing.T) {
	t.Parallel()

	expression, err := filter.Parse(`profile['a:1 OR b'] == 'x'`)
	if err != nil {
		t.Fatal(err)
	}
	binary, ok := expression.(*filter.BinaryExpr)
	if !ok {
		t.Fatalf("Parse() = %T, want *filter.BinaryExpr", expression)
	}
	path, err := binary.Path()
	if err != nil {
		t.Fatalf("Path() = %v, want nil", err)
	}
	if got := pathText(path); got != "profile/a:1 OR b" {
		t.Fatalf("Path() = %s, want the key verbatim", got)
	}
}
