//go:build ignore

// Run from the workspace root: go run scripts/vectorstore-matrix.go -write
// The catalog is a projection of exported Go operations, not a declaration
// of provider capabilities or evidence that a service has been exercised.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

const begin = "<!-- vectorstore-catalog:begin -->"
const end = "<!-- vectorstore-catalog:end -->"
const vectorstore = "github.com/Tangerg/scope/core/vectorstore"

type surface struct {
	owner                        string
	upsert, query, retrieve      bool
	scan, deleteMatching, create bool
}

func (s surface) active() bool {
	return s.upsert || s.query || s.retrieve
}
func (s surface) state() string {
	switch {
	case s.upsert && s.query:
		return "Native vectors"
	case s.upsert && s.scan && !s.query && !s.retrieve:
		return "Native vectors (explicit scan only)"
	case s.retrieve && !s.upsert && !s.query:
		return "Managed retrieval"
	default:
		return "Mixed/incomplete; inspect owner"
	}
}

func receiver(x ast.Expr) string {
	switch t := x.(type) {
	case *ast.StarExpr:
		return receiver(t.X)
	case *ast.Ident:
		return t.Name
	}
	return ""
}

func hasParameter(fn *ast.FuncDecl, imports map[string]string, typename string) bool {
	for _, field := range fn.Type.Params.List {
		t := field.Type
		if ptr, ok := t.(*ast.StarExpr); ok {
			t = ptr.X
		}
		sel, ok := t.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		id, ok := sel.X.(*ast.Ident)
		if ok && imports[id.Name] == vectorstore && sel.Sel.Name == typename {
			return true
		}
	}
	return false
}

func inspect(path string, records map[string]*surface) error {
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		return err
	}
	imports := map[string]string{}
	for _, im := range f.Imports {
		p, err := strconv.Unquote(im.Path.Value)
		if err != nil {
			return err
		}
		name := filepath.Base(p)
		if im.Name != nil {
			name = im.Name.Name
		}
		imports[name] = p
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || !fn.Name.IsExported() {
			continue
		}
		owner := receiver(fn.Recv.List[0].Type)
		if owner != "Store" && owner != "Retriever" && owner != "Corpus" {
			continue
		}
		key := filepath.Join(filepath.Dir(path), owner)
		s := records[key]
		if s == nil {
			s = &surface{owner: filepath.Dir(path)}
			records[key] = s
		}
		switch fn.Name.Name {
		case "Upsert":
			s.upsert = hasParameter(fn, imports, "Batch")
		case "Query":
			s.query = hasParameter(fn, imports, "Query")
		case "Retrieve":
			s.retrieve = true
		case "Scan":
			s.scan = true
		case "DeleteMatching":
			s.deleteMatching = true
		case "CreateDocument":
			s.create = true
		}
	}
	return nil
}

func moduleRow(root, dir string) (string, error) {
	records := map[string]*surface{}
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "internal" || entry.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		return inspect(path, records)
	})
	if err != nil {
		return "", err
	}
	var states, apis, docs []string
	add := func(xs *[]string, x string) {
		if !slices.Contains(*xs, x) {
			*xs = append(*xs, x)
		}
	}
	for _, s := range records {
		if !s.active() {
			continue
		}
		add(&states, s.state())
		for _, op := range []struct {
			name    string
			present bool
		}{{"Upsert", s.upsert}, {"Query", s.query}, {"Scan", s.scan}, {"DeleteMatching", s.deleteMatching}, {"CreateDocument", s.create}, {"Retrieve", s.retrieve}} {
			if op.present {
				add(&apis, "`"+op.name+"`")
			}
		}
		ref, err := filepath.Rel(filepath.Join(root, "vectorstores"), filepath.Join(s.owner, "doc.go"))
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(filepath.Join(s.owner, "doc.go")); err != nil {
			return "", err
		}
		add(&docs, "["+filepath.Base(s.owner)+"]("+filepath.ToSlash(ref)+")")
	}
	if len(states) == 0 {
		return "", fmt.Errorf("no public provider operation found in %s", dir)
	}
	sort.Strings(states)
	sort.Strings(apis)
	sort.Strings(docs)
	mod, err := filepath.Rel(root, dir)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("| `%s` | %s | %s | %s |\n", filepath.ToSlash(mod), strings.Join(states, "; "), strings.Join(apis, ", "), strings.Join(docs, ", ")), nil
}

func render(root string) ([]byte, error) {
	var modules []string
	for _, family := range []string{"vectorstores", "retrievers"} {
		base := filepath.Join(root, family)
		entries, err := os.ReadDir(base)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() {
				d := filepath.Join(base, e.Name())
				if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
					modules = append(modules, d)
				} else if !os.IsNotExist(err) {
					return nil, err
				}
			}
		}
	}
	sort.Strings(modules)
	if len(modules) == 0 {
		return nil, fmt.Errorf("no provider modules under %s", root)
	}
	var out bytes.Buffer
	out.WriteString(begin + "\n\n| Module | Public boundary | Index, query, or explicit scan operations | Contract owner |\n|---|---|---|---|\n")
	for _, dir := range modules {
		r, err := moduleRow(root, dir)
		if err != nil {
			return nil, err
		}
		out.WriteString(r)
	}
	out.WriteString("\n" + end)
	return out.Bytes(), nil
}

func run() error {
	root := flag.String("root", ".", "workspace root")
	write := flag.Bool("write", false, "refresh the existing README catalog")
	check := flag.Bool("check", false, "reject a stale catalog")
	flag.Parse()
	if *write && *check {
		return fmt.Errorf("choose -write or -check")
	}
	absolute, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	matrix, err := render(absolute)
	if err != nil {
		return err
	}
	if !*write && !*check {
		_, err = os.Stdout.Write(append(matrix, '\n'))
		return err
	}
	path := filepath.Join(absolute, "vectorstores", "README.md")
	previous, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	first := bytes.Index(previous, []byte(begin))
	last := bytes.Index(previous, []byte(end))
	if first < 0 || last < first || bytes.Count(previous, []byte(begin)) != 1 || bytes.Count(previous, []byte(end)) != 1 {
		return fmt.Errorf("invalid catalog markers in %s", path)
	}
	if bytes.Equal(previous[first:last+len(end)], matrix) {
		return nil
	}
	if *check {
		return fmt.Errorf("stale vector catalog; run go run scripts/vectorstore-matrix.go -write")
	}
	next := append(bytes.Clone(previous[:first]), matrix...)
	next = append(next, previous[last+len(end):]...)
	return os.WriteFile(path, next, 0644)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
