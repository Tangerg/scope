package chatclient

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
)

func TestParseTemplateRejectsEmptyAndMalformedSource(t *testing.T) {
	for _, source := range []string{"", " \n\t", "{{"} {
		template, err := ParseTemplate(source)
		if template != nil || !errors.Is(err, ErrInvalidTemplate) {
			t.Fatalf("ParseTemplate(%q) = (%v, %v), want nil and ErrInvalidTemplate", source, template, err)
		}
	}
}

func TestTemplateRenderIsImmutableAndMissingKeysFail(t *testing.T) {
	template, err := ParseTemplate("Hello {{.Name}} from {{.Place}}")
	if err != nil {
		t.Fatal(err)
	}
	if template.Source() != "Hello {{.Name}} from {{.Place}}" {
		t.Fatalf("Source() = %q", template.Source())
	}

	first, err := template.Render(map[string]any{"Name": "Ada", "Place": "London"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := template.Render(struct {
		Name  string
		Place string
	}{Name: "Lin", Place: "Shanghai"})
	if err != nil {
		t.Fatal(err)
	}
	if first != "Hello Ada from London" || second != "Hello Lin from Shanghai" {
		t.Fatalf("renders = %q / %q", first, second)
	}
	if _, err := template.Render(map[string]any{"Name": "Ada"}); !errors.Is(err, ErrInvalidTemplate) {
		t.Fatalf("missing-key error = %v, want ErrInvalidTemplate", err)
	}
}

func TestTemplateRequireUsesParsedTopLevelFields(t *testing.T) {
	template, err := ParseTemplate(`{{if .Enabled}}{{ .User.Name }}{{end}}{{range .Items}}{{.}}{{end}}`)
	if err != nil {
		t.Fatal(err)
	}
	if requireErr := template.Require("User", "Enabled", "Items", "User"); requireErr != nil {
		t.Fatalf("Require present fields: %v", requireErr)
	}
	err = template.Require("Zed", "Name", "Name")
	if !errors.Is(err, ErrInvalidTemplate) || !strings.Contains(err.Error(), "Name, Zed") {
		t.Fatalf("Require missing fields error = %v", err)
	}
	if err := template.Require(""); !errors.Is(err, ErrInvalidTemplate) {
		t.Fatalf("Require empty name error = %v", err)
	}
}

func TestTemplateRequireRecognizesRootFields(t *testing.T) {
	for _, source := range []string{
		`{{$.Query}}`,
		`{{with .Target}}{{$.Query}}{{end}}`,
		`{{printf "%s" $.Query}}`,
	} {
		t.Run(source, func(t *testing.T) {
			template, err := ParseTemplate(source)
			if err != nil {
				t.Fatal(err)
			}
			if requireErr := template.Require("Query"); requireErr != nil {
				t.Fatal(requireErr)
			}
			rendered, err := template.Render(map[string]string{"Query": "question", "Target": "index"})
			if err != nil || rendered != "question" {
				t.Fatalf("Render = %q, %v", rendered, err)
			}
		})
	}
	template, err := ParseTemplate(`{{$.User.Name}}{{$item := .User}}{{$item.Name}}`)
	if err != nil {
		t.Fatal(err)
	}
	if requireErr := template.Require("User"); requireErr != nil {
		t.Fatal(requireErr)
	}
	if requireErr := template.Require("Name"); !errors.Is(requireErr, ErrInvalidTemplate) {
		t.Fatalf("Require nested field = %v, want ErrInvalidTemplate", requireErr)
	}
}

func TestTemplateBuildsValidatedSystemAndUserMessages(t *testing.T) {
	template, err := ParseTemplate("Hello {{.Name}}")
	if err != nil {
		t.Fatal(err)
	}
	system, err := template.SystemMessage(map[string]string{"Name": "system"})
	if err != nil {
		t.Fatal(err)
	}
	if system.Role != chat.RoleSystem || system.Text() != "Hello system" {
		t.Fatalf("system message = %#v", system)
	}

	image, err := media.NewBytes("image/png", []byte{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	user, err := template.UserMessage(map[string]string{"Name": "user"}, image)
	if err != nil {
		t.Fatal(err)
	}
	if user.Role != chat.RoleUser || user.Text() != "Hello user" || len(user.Parts) != 2 || user.Parts[1].Media != image {
		t.Fatalf("user message = %#v", user)
	}
}

func TestTemplateAllowsMediaOnlyUserButRejectsEmptyMessages(t *testing.T) {
	template, err := ParseTemplate(`{{if .Show}}text{{end}}`)
	if err != nil {
		t.Fatal(err)
	}
	image, err := media.NewURI("image/png", "https://example.com/image.png")
	if err != nil {
		t.Fatal(err)
	}
	user, err := template.UserMessage(map[string]bool{"Show": false}, image)
	if err != nil {
		t.Fatal(err)
	}
	if len(user.Parts) != 1 || user.Parts[0].Kind != chat.PartMedia {
		t.Fatalf("media-only message = %#v", user)
	}
	if _, err := template.UserMessage(map[string]bool{"Show": false}); !errors.Is(err, ErrInvalidTemplate) {
		t.Fatalf("empty user error = %v", err)
	}
	if _, err := template.SystemMessage(map[string]bool{"Show": false}); !errors.Is(err, ErrInvalidTemplate) {
		t.Fatalf("empty system error = %v", err)
	}
	if _, err := template.UserMessage(map[string]bool{"Show": false}, nil); !errors.Is(err, ErrInvalidTemplate) {
		t.Fatalf("nil media error = %v", err)
	}
}

func TestTemplateSupportsConcurrentPerRenderData(t *testing.T) {
	template, err := ParseTemplate("{{.Value}}")
	if err != nil {
		t.Fatal(err)
	}
	const count = 50
	results := make([]string, count)
	errorsFound := make(chan error, count)
	var wait sync.WaitGroup
	for i := range count {
		wait.Go(func() {
			value := string(rune('A' + i%26))
			var renderErr error
			results[i], renderErr = template.Render(map[string]string{"Value": value})
			errorsFound <- renderErr
		})
	}
	wait.Wait()
	close(errorsFound)
	for renderErr := range errorsFound {
		if renderErr != nil {
			t.Fatal(renderErr)
		}
	}
	for i, result := range results {
		want := string(rune('A' + i%26))
		if !reflect.DeepEqual(result, want) {
			t.Fatalf("result[%d] = %q, want %q", i, result, want)
		}
	}
}

func TestNilTemplateMethodsFailSafely(t *testing.T) {
	var template *Template
	if template.Source() != "" {
		t.Fatalf("nil Source() = %q", template.Source())
	}
	if _, err := template.Render(nil); !errors.Is(err, ErrInvalidTemplate) {
		t.Fatalf("nil Render error = %v", err)
	}
	if err := template.Require("Name"); !errors.Is(err, ErrInvalidTemplate) {
		t.Fatalf("nil Require error = %v", err)
	}
}
