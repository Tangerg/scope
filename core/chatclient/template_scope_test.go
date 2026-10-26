package chatclient

import (
	"errors"
	"testing"
)

func TestTemplateRequireTracksRootScope(t *testing.T) {
	for _, test := range []struct {
		name    string
		source  string
		present []string
		missing []string
	}{
		{
			name: "with changes dot", source: `{{with .User}}{{.Name}}{{end}}`,
			present: []string{"User"}, missing: []string{"Name"},
		},
		{
			name: "range changes dot", source: `{{range .Items}}{{.Name}}{{end}}`,
			present: []string{"Items"}, missing: []string{"Name"},
		},
		{
			name: "with preserves root identity", source: `{{with .}}{{.Name}}{{end}}`,
			present: []string{"Name"},
		},
		{
			name: "with else preserves dot", source: `{{with .User}}{{.Name}}{{else}}{{.Fallback}}{{end}}`,
			present: []string{"User", "Fallback"}, missing: []string{"Name"},
		},
		{
			name: "range else preserves dot", source: `{{range .Items}}{{.Name}}{{else}}{{.Fallback}}{{end}}`,
			present: []string{"Items", "Fallback"}, missing: []string{"Name"},
		},
		{
			name: "root alias", source: `{{$root := .}}{{with .User}}{{$root.Name}}{{end}}`,
			present: []string{"User", "Name"},
		},
		{
			name: "root variable shadowing", source: `{{$ := .User}}{{$.Name}}`,
			present: []string{"User"}, missing: []string{"Name"},
		},
		{
			name: "root variable assignment", source: `{{$ = .User}}{{$.Name}}`,
			present: []string{"User"}, missing: []string{"Name"},
		},
		{
			name: "nested field chain", source: `{{(.User).Name}}`,
			present: []string{"User"}, missing: []string{"Name"},
		},
		{
			name: "unused definition", source: `{{define "unused"}}{{.Query}}{{end}}constant`,
			missing: []string{"Query"},
		},
		{
			name: "called root template", source: `{{define "query"}}{{.Query}}{{end}}{{template "query" .}}`,
			present: []string{"Query"},
		},
		{
			name: "called nested template", source: `{{define "user"}}{{.Name}}{{$.Name}}{{end}}{{template "user" .User}}`,
			present: []string{"User"}, missing: []string{"Name"},
		},
		{
			name: "called without data", source: `{{define "query"}}{{$.Query}}{{end}}{{template "query"}}`,
			missing: []string{"Query"},
		},
		{
			name: "same template with different roots", source: `{{define "name"}}{{.Name}}{{end}}{{template "name" .User}}{{template "name" .}}`,
			present: []string{"User", "Name"},
		},
		{
			name: "root passed out of nested scope", source: `{{define "query"}}{{.Query}}{{end}}{{with .User}}{{template "query" $}}{{end}}`,
			present: []string{"User", "Query"},
		},
		{
			name: "recursive template", source: `{{define "query"}}{{.Query}}{{if .Next}}{{template "query" .Next}}{{end}}{{end}}{{template "query" .}}`,
			present: []string{"Query", "Next"},
		},
		{
			name: "local alias does not escape", source: `{{$value := .User}}{{if .Enabled}}{{$value := $}}{{$value.Name}}{{end}}{{$value.Other}}`,
			present: []string{"User", "Enabled", "Name"}, missing: []string{"Other"},
		},
		{
			name: "branch assignment survives", source: `{{$value := .User}}{{if .Enabled}}{{$value = $}}{{end}}{{$value.Name}}`,
			present: []string{"User", "Enabled", "Name"},
		},
		{
			name: "both branches replace root alias", source: `{{$value := $}}{{if .Enabled}}{{$value = .User}}{{else}}{{$value = .Other}}{{end}}{{$value.Name}}`,
			present: []string{"Enabled", "User", "Other"}, missing: []string{"Name"},
		},
		{
			name: "range variable is an element", source: `{{range $item := .Items}}{{$item.Name}}{{end}}`,
			present: []string{"Items"}, missing: []string{"Name"},
		},
		{
			name: "range assignment reaches next iteration", source: `{{$value := .User}}{{range .Items}}{{$value.Name}}{{$value = $}}{{end}}`,
			present: []string{"User", "Items", "Name"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			prompt, err := ParseTemplate(test.source)
			if err != nil {
				t.Fatal(err)
			}
			if err := prompt.Require(test.present...); err != nil {
				t.Fatalf("Require(%q) = %v", test.present, err)
			}
			for _, field := range test.missing {
				if err := prompt.Require(field); !errors.Is(err, ErrInvalidTemplate) {
					t.Errorf("Require(%q) = %v, want ErrInvalidTemplate", field, err)
				}
			}
		})
	}
}

func TestTemplateRequireRejectsFieldsAbsentFromRenderedInput(t *testing.T) {
	t.Run("nested field", func(t *testing.T) {
		prompt, err := ParseTemplate(`{{with .User}}{{.Name}}{{end}}`)
		if err != nil {
			t.Fatal(err)
		}
		data := struct{ User struct{ Name string } }{}
		data.User.Name = "nested"
		rendered, err := prompt.Render(data)
		if err != nil || rendered != "nested" {
			t.Fatalf("Render = %q, %v", rendered, err)
		}
		if err := prompt.Require("Name"); !errors.Is(err, ErrInvalidTemplate) {
			t.Fatalf("Require root Name = %v", err)
		}
	})
	t.Run("unused definition", func(t *testing.T) {
		prompt, err := ParseTemplate(`{{define "unused"}}{{.Query}}{{end}}constant`)
		if err != nil {
			t.Fatal(err)
		}
		rendered, err := prompt.Render(struct{}{})
		if err != nil || rendered != "constant" {
			t.Fatalf("Render = %q, %v", rendered, err)
		}
		if err := prompt.Require("Query"); !errors.Is(err, ErrInvalidTemplate) {
			t.Fatalf("Require Query = %v", err)
		}
	})
}

type templateControlInput struct {
	Name  string
	User  templateControlItem
	Items []templateControlItem
}

type templateControlItem struct {
	Name  string
	Skip  bool
	Inner []int
}

func TestTemplateRequireTracksControlFlow(t *testing.T) {
	for _, test := range []struct {
		name     string
		source   string
		items    []templateControlItem
		rendered string
		rootName bool
	}{
		{
			name: "break skips root assignment", source: `{{$value := .User}}{{range .Items}}{{break}}{{$value = $}}{{end}}{{$value.Name}}`,
			items: []templateControlItem{{}, {}}, rendered: "nested",
		},
		{
			name: "continue skips root assignment", source: `{{$value := .User}}{{range .Items}}{{continue}}{{$value = $}}{{end}}{{$value.Name}}`,
			items: []templateControlItem{{}, {}}, rendered: "nested",
		},
		{
			name: "break skips alias reset", source: `{{$value := .User}}{{range .Items}}{{$value = $}}{{break}}{{$value = .}}{{end}}{{$value.Name}}`,
			items: []templateControlItem{{}, {}}, rendered: "root", rootName: true,
		},
		{
			name: "continue skips alias reset", source: `{{$value := .User}}{{range .Items}}{{$value = $}}{{continue}}{{$value = .}}{{end}}{{$value.Name}}`,
			items: []templateControlItem{{}, {}}, rendered: "root", rootName: true,
		},
		{
			name: "conditional break cannot feed next iteration", source: `{{$value := .User}}{{range .Items}}{{if .Skip}}{{$value = $}}{{break}}{{end}}{{$value.Name}}{{end}}`,
			items: []templateControlItem{{}, {Skip: true}}, rendered: "nested",
		},
		{
			name: "conditional continue feeds next iteration", source: `{{$value := .User}}{{range .Items}}{{if .Skip}}{{$value = $}}{{continue}}{{end}}{{$value.Name}}{{end}}`,
			items: []templateControlItem{{Skip: true}, {}}, rendered: "root", rootName: true,
		},
		{
			name: "conditional break retains exit alias", source: `{{$value := .User}}{{range .Items}}{{if .Skip}}{{$value = $}}{{break}}{{end}}{{$value = .}}{{end}}{{$value.Name}}`,
			items: []templateControlItem{{Skip: true}}, rendered: "root", rootName: true,
		},
		{
			name: "both branches stop the body", source: `{{$value := .User}}{{range .Items}}{{if .Skip}}{{continue}}{{else}}{{break}}{{end}}{{$value = $}}{{end}}{{$value.Name}}`,
			items: []templateControlItem{{Skip: true}, {}}, rendered: "nested",
		},
		{
			name: "local alias cannot escape through break", source: `{{$value := .User}}{{range .Items}}{{with .}}{{$value := $}}{{break}}{{end}}{{end}}{{$value.Name}}`,
			items: []templateControlItem{{}}, rendered: "nested",
		},
		{
			name: "nested break stays in inner range", source: `{{$value := .User}}{{range .Items}}{{range .Inner}}{{break}}{{end}}{{$value = $}}{{end}}{{$value.Name}}`,
			items: []templateControlItem{{Inner: []int{1}}}, rendered: "root", rootName: true,
		},
		{
			name: "nested continue stays in inner range", source: `{{$value := .User}}{{range .Items}}{{range .Inner}}{{continue}}{{end}}{{$value = $}}{{end}}{{$value.Name}}`,
			items: []templateControlItem{{Inner: []int{1, 2}}}, rendered: "root", rootName: true,
		},
		{
			name: "range else break stops that range", source: `{{$value := .User}}{{range .Items}}{{range .Inner}}{{else}}{{$value = $}}{{break}}{{end}}{{$value = .}}{{end}}{{$value.Name}}`,
			items: []templateControlItem{{Name: "item"}}, rendered: "item",
		},
		{
			name: "range else continue reaches outer iteration", source: `{{$value := .User}}{{range .Items}}{{range .Inner}}{{else}}{{$value = $}}{{continue}}{{end}}{{$value = .}}{{end}}{{$value.Name}}`,
			items: []templateControlItem{{Name: "item"}}, rendered: "root", rootName: true,
		},
		{
			name: "called template control stays in its range", source: `{{define "loop"}}{{range .Items}}{{break}}{{end}}{{end}}{{$value := .User}}{{template "loop" .}}{{$value = $}}{{$value.Name}}`,
			items: []templateControlItem{{}}, rendered: "root", rootName: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			prompt, err := ParseTemplate(test.source)
			if err != nil {
				t.Fatal(err)
			}
			rendered, err := prompt.Render(templateControlInput{
				Name: "root", User: templateControlItem{Name: "nested"}, Items: test.items,
			})
			if err != nil || rendered != test.rendered {
				t.Fatalf("Render = %q, %v; want %q", rendered, err, test.rendered)
			}
			err = prompt.Require("Name")
			if test.rootName && err != nil {
				t.Fatalf("Require root Name = %v", err)
			}
			if !test.rootName && !errors.Is(err, ErrInvalidTemplate) {
				t.Fatalf("Require root Name = %v, want ErrInvalidTemplate", err)
			}
		})
	}
}
