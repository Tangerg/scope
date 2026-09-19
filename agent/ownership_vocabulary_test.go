package agent

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// Check executable vocabulary rather than prose explaining excluded concerns.
// Ordinary string payloads remain opaque; only declared constants and wire
// field names establish vocabulary in addition to identifiers and imports.
func agentOwnershipViolations(packagePath string, file *ast.File) []string {
	var violations []string
	check := func(value string) {
		for _, word := range ownershipWords(value) {
			switch strings.TrimSuffix(word, "s") {
			case "billing", "tenant", "session", "pricing", "dashboard", "marketplace":
				violations = append(violations, fmt.Sprintf("Host-owned vocabulary %q", word))
			case "model", "tool", "prompt", "conversation", "workflow", "planner", "goap":
				if !isPackageOrChild(packagePath, "strategy") {
					violations = append(violations, fmt.Sprintf("Strategy-owned vocabulary %q outside strategy packages", word))
				}
			}
		}
	}
	for _, imported := range file.Imports {
		path, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			panic(err)
		}
		if isPackageOrChild(path, "net/http") || isPackageOrChild(path, "database/sql") {
			violations = append(violations, fmt.Sprintf("Host-owned transport or storage import %q", path))
		}
		check(path)
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.ImportSpec:
			return false
		case *ast.Ident:
			check(node.Name)
		case *ast.Field:
			if node.Tag != nil {
				tag, err := strconv.Unquote(node.Tag.Value)
				if err != nil {
					panic(err)
				}
				name, _, _ := strings.Cut(reflect.StructTag(tag).Get("json"), ",")
				check(name)
			}
		case *ast.GenDecl:
			if node.Tok == token.CONST {
				ast.Inspect(node, func(child ast.Node) bool {
					if literal, ok := child.(*ast.BasicLit); ok && literal.Kind == token.STRING {
						value, err := strconv.Unquote(literal.Value)
						if err != nil {
							panic(err)
						}
						check(value)
					}
					return true
				})
			}
		}
		return true
	})
	slices.Sort(violations)
	return slices.Compact(violations)
}

func ownershipWords(value string) []string {
	var separated strings.Builder
	runes := []rune(value)
	for index, current := range runes {
		if index > 0 && unicode.IsUpper(current) {
			previous := runes[index-1]
			wordStart := unicode.IsLower(previous) || unicode.IsDigit(previous)
			acronymEnd := unicode.IsUpper(previous) && index+1 < len(runes) && unicode.IsLower(runes[index+1])
			if wordStart || acronymEnd {
				separated.WriteByte(' ')
			}
		}
		separated.WriteRune(unicode.ToLower(current))
	}
	return strings.FieldsFunc(separated.String(), func(character rune) bool { return !unicode.IsLetter(character) })
}

func TestAgentOwnershipVocabularyRules(t *testing.T) {
	for _, test := range []struct {
		name, path, source string
		want               []string
	}{
		{name: "product field", path: ".", source: "type State struct { TenantID string }", want: []string{`Host-owned vocabulary "tenant"`}},
		{name: "product in strategy", path: "strategy/interaction", source: "type BillingPolicy struct{}", want: []string{`Host-owned vocabulary "billing"`}},
		{name: "wire field", path: ".", source: "type State struct { Value string `json:\"session_id,omitempty\"` }", want: []string{`Host-owned vocabulary "session"`}},
		{name: "constant", path: ".", source: `const operation = "model.call"`, want: []string{`Strategy-owned vocabulary "model" outside strategy packages`}},
		{name: "acronym", path: ".", source: "type HTTPModelResponse struct{}", want: []string{`Strategy-owned vocabulary "model" outside strategy packages`}},
		{name: "private helper", path: "internal/protocol", source: "func invokeTool() {}", want: []string{`Strategy-owned vocabulary "tool" outside strategy packages`}},
		{name: "aliased transport", path: "strategy/interaction", source: `import transport "net/http"`, want: []string{`Host-owned transport or storage import "net/http"`}},
		{name: "blank storage", path: ".", source: `import _ "database/sql/driver"`, want: []string{`Host-owned transport or storage import "database/sql/driver"`}},
		{name: "product import", path: ".", source: `import _ "example.com/billing"`, want: []string{`Host-owned vocabulary "billing"`}},
		{name: "strategy", path: "strategy/interaction", source: "type ModelToolPolicy struct{}"},
		{name: "strategy helper", path: "strategy/internal/childcall", source: "type ModelInvocation struct{}"},
		{name: "generic delegation", path: "agenttest", source: "type Store struct { delegate TreeDurability }"},
		{name: "generic plan", path: ".", source: "type childStartPlan struct{}; const phase = \"planned\""},
		{name: "substrings", path: ".", source: "type Remodeling struct { ToolingCount int; Tenanted bool }"},
		{name: "opaque payload and comment", path: ".", source: "// Billing and models belong elsewhere.\nfunc payload() string { return `billing model tool` }"},
	} {
		t.Run(test.name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", "package fixture\n"+test.source, parser.ParseComments)
			if err != nil {
				t.Fatal(err)
			}
			if got := agentOwnershipViolations(test.path, file); !slices.Equal(got, test.want) {
				t.Fatalf("violations = %v, want %v", got, test.want)
			}
		})
	}
}
