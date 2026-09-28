// Package arch holds architecture-fitness tests for the core package family.
package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/moderation"
	"github.com/Tangerg/scope/core/rerank"
	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestDocumentRemainsPureData(t *testing.T) {
	typ := reflect.TypeFor[document.Document]()
	wantFields := []string{"ID", "Text", "Media", "Metadata"}
	for index, name := range wantFields {
		if index >= typ.NumField() {
			t.Fatalf("document.Document missing field %d (%s)", index, name)
		}
		if got := typ.Field(index).Name; got != name {
			t.Fatalf("document.Document field %d = %v, want %s", index, got, name)
		}
	}
	if typ.NumField() != len(wantFields) {
		t.Fatalf("document.Document has %d fields, want %d", typ.NumField(), len(wantFields))
	}
	metadataField, _ := typ.FieldByName("Metadata")
	if metadataField.Type != reflect.TypeFor[metadata.Map]() {
		t.Fatalf("document.Metadata type = %v, want metadata.Map", metadataField.Type)
	}

	pointer := reflect.PointerTo(typ)
	for _, forbidden := range []string{"EnsureID", "Format", "FormatByMetadataMode", "FormatWith"} {
		if _, ok := pointer.MethodByName(forbidden); ok {
			t.Errorf("document.Document must not expose runtime method %s", forbidden)
		}
	}
}

func TestRemovedConvenienceSurfaceDoesNotReturn(t *testing.T) {
	forbidden := map[string]map[string]bool{
		"document":    {"Reader": true, "Writer": true},
		"metadata":    {"New": true},
		"vectorstore": {"AcceptAllScores": true, "NewDocumentWriter": true},
		"image":       {"Image": true, "NewImage": true, "ResponseFormat": true},
	}
	for packageName, names := range forbidden {
		assertTopLevelNamesAbsent(t, packageName, names)
	}
}

func TestVectorStoreCapabilitiesRemainSmall(t *testing.T) {
	want := map[reflect.Type]string{
		reflect.TypeFor[vectorstore.Indexer]():       "Index",
		reflect.TypeFor[vectorstore.Searcher]():      "Search",
		reflect.TypeFor[vectorstore.IDDeleter]():     "DeleteIDs",
		reflect.TypeFor[vectorstore.FilterDeleter](): "DeleteWhere",
	}
	for typ, method := range want {
		if typ.NumMethod() != 1 || typ.Method(0).Name != method {
			t.Errorf("%v methods changed: want only %s", typ, method)
		}
	}

	root := filepath.Join(coreRoot(t), "vectorstore")
	forbidden := map[string]bool{
		"Store": true, "StoreMetadata": true,
		"Creator": true, "CreateRequest": true,
		"Retriever": true, "RetrievalRequest": true,
		"Deleter": true, "DeleteRequest": true,
	}
	fset := token.NewFileSet()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(root, entry.Name())
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, declaration := range file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.TYPE {
				continue
			}
			for _, specification := range general.Specs {
				name := specification.(*ast.TypeSpec).Name.Name
				if forbidden[name] {
					t.Errorf("core/vectorstore must not reintroduce %s", name)
				}
			}
		}
	}
}

func TestEmbeddingSPIRemainsMinimal(t *testing.T) {
	assertSingleMethodInterface(t, reflect.TypeFor[embedding.Model](), "Call")
	forbidden := map[string]bool{
		"ModelMetadata": true, "Client": true,
		"ClientRequest": true, "ClientCaller": true,
		"Dimensioner": true, "DimensionFunc": true,
		"EncodingFormat": true, "ModalityType": true,
		"Middleware": true, "MiddlewareChain": true, "Handler": true,
	}
	for name := range map[string]bool{
		"GetDimensions":     true,
		"ProbeDimensions":   true,
		"ResolveDimensions": true,
		"NewClient":         true,
		"NewClientRequest":  true, "NewClientFromRequest": true,
		"NewMiddlewareChain": true,
	} {
		forbidden[name] = true
	}
	assertTopLevelNamesAbsent(t, "embedding", forbidden)
}

func TestChatRequestKeepsProtocolConcernsAtTheirOwner(t *testing.T) {
	request := reflect.TypeFor[chat.Request]()
	if _, exists := request.FieldByName("ToolChoice"); !exists {
		t.Fatal("chat.Request must own ToolChoice")
	}
	if _, duplicate := reflect.TypeFor[chat.Options]().FieldByName("ToolChoice"); duplicate {
		t.Fatal("chat.Options duplicates Request.ToolChoice")
	}
}

// Compile-time anchors detect removed SPIs; the derived inventory detects
// additions that have not joined the family rules.
var modalitySPIs = map[string]map[reflect.Type]string{
	"chat": {
		reflect.TypeFor[chat.Model]():    "Call",
		reflect.TypeFor[chat.Streamer](): "Stream",
	},
	"embedding":     {reflect.TypeFor[embedding.Model](): "Call"},
	"image":         {reflect.TypeFor[image.Model](): "Call"},
	"moderation":    {reflect.TypeFor[moderation.Model](): "Call"},
	"rerank":        {reflect.TypeFor[rerank.Model](): "Call"},
	"speech":        {reflect.TypeFor[speech.Model](): "Call", reflect.TypeFor[speech.Streamer](): "Stream"},
	"transcription": {reflect.TypeFor[transcription.Model](): "Call"},
}

// modalitiesWithOwnProtocolErrors names the only modality allowed to extend the
// shared error vocabulary, because its wire additionally models tool calls,
// parts, and usage. Every other modality is bound to the exact triple by
// default; exempting a new one requires naming it here.
var modalitiesWithOwnProtocolErrors = map[string]bool{"chat": true}

func TestModalitySPIsRemainMinimal(t *testing.T) {
	assertModalityInventoryIsComplete(t)

	for packageName, spis := range modalitySPIs {
		for typ, method := range spis {
			if typ.NumMethod() != 1 || typ.Method(0).Name != method {
				t.Errorf("%v methods changed: want only %s", typ, method)
			}
		}
		assertMinimalModalityPackage(t, packageName)
	}
}

func TestModalityErrorVocabularyRemainsUnified(t *testing.T) {
	assertModalityInventoryIsComplete(t)

	shared := []string{"ErrInvalidOptions", "ErrInvalidRequest", "ErrInvalidResponse"}
	for _, packageName := range modalityPackages(t) {
		t.Run(packageName, func(t *testing.T) {
			got := exportedErrorSentinelNames(t, packageName)
			if modalitiesWithOwnProtocolErrors[packageName] {
				for _, name := range shared {
					if !slices.Contains(got, name) {
						t.Errorf("core/%s exported error sentinels = %v, missing %s", packageName, got, name)
					}
				}
				return
			}
			if !slices.Equal(got, shared) {
				t.Fatalf("core/%s exported error sentinels = %v, want exactly %v", packageName, got, shared)
			}
		})
	}
}

func TestModalityCreationTimestampsUseOneRepresentation(t *testing.T) {
	metadataTypes := map[string]reflect.Type{
		"chat":          reflect.TypeFor[chat.ResponseMetadata](),
		"embedding":     reflect.TypeFor[embedding.ResponseMetadata](),
		"image":         reflect.TypeFor[image.ResponseMetadata](),
		"moderation":    reflect.TypeFor[moderation.ResponseMetadata](),
		"rerank":        reflect.TypeFor[rerank.ResponseMetadata](),
		"speech":        reflect.TypeFor[speech.ResponseMetadata](),
		"transcription": reflect.TypeFor[transcription.ResponseMetadata](),
	}
	for packageName, typ := range metadataTypes {
		field, exists := typ.FieldByName("CreatedAt")
		if !exists {
			continue
		}
		if field.Type != reflect.TypeFor[time.Time]() || field.Tag.Get("json") != "created_at,omitzero" {
			t.Errorf("core/%s ResponseMetadata.CreatedAt = (%v, %q), want time.Time with created_at,omitzero", packageName, field.Type, field.Tag.Get("json"))
		}
	}
}

func assertModalityInventoryIsComplete(t *testing.T) {
	t.Helper()
	anchored := slices.Sorted(maps.Keys(modalitySPIs))
	published := modalityPackages(t)
	if !slices.Equal(anchored, published) {
		t.Fatalf(
			"modality inventory is stale: anchored %v, packages publishing a Model SPI %v",
			anchored, published,
		)
	}
}

func modalityPackages(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(coreRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "internal" {
			continue
		}
		if publishesModelSPI(t, entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	slices.Sort(names)
	return names
}

func publishesModelSPI(t *testing.T, packagePath string) bool {
	t.Helper()
	for _, parsed := range parsePackageFiles(t, packagePath) {
		for _, declaration := range parsed.file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.TYPE {
				continue
			}
			for _, specification := range general.Specs {
				typeSpec, ok := specification.(*ast.TypeSpec)
				if !ok || typeSpec.Name.Name != "Model" {
					continue
				}
				if _, isInterface := typeSpec.Type.(*ast.InterfaceType); isInterface {
					return true
				}
			}
		}
	}
	return false
}

func TestCoreDoesNotOwnProviderCatalogData(t *testing.T) {
	root := filepath.Join(coreRoot(t), "chat")
	forbidden := map[string]bool{
		"ModelInfo": true, "Pricing": true, "Reasoning": true,
		"Limits": true, "Modality": true, "Modalities": true,
	}
	fset := token.NewFileSet()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(root, entry.Name())
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, declaration := range file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.TYPE {
				continue
			}
			for _, specification := range general.Specs {
				name := specification.(*ast.TypeSpec).Name.Name
				if forbidden[name] {
					t.Errorf("Core must not own provider catalog type %s", name)
				}
			}
		}
	}
}

func assertMinimalModalityPackage(t *testing.T, packageName string) {
	t.Helper()
	forbidden := map[string]bool{
		"ModelMetadata": true, "Client": true,
		"ClientRequest": true, "ClientCaller": true, "ClientStreamer": true,
		"Middleware": true, "MiddlewareChain": true,
		"Handler": true, "HandlerFunc": true,
	}
	for name := range map[string]bool{
		"NewClient": true, "NewClientRequest": true,
		"NewClientFromRequest": true, "NewMiddlewareChain": true,
	} {
		forbidden[name] = true
	}
	assertTopLevelNamesAbsent(t, packageName, forbidden)
}

func exportedErrorSentinelNames(t *testing.T, packageName string) []string {
	t.Helper()
	var names []string
	for _, parsed := range parsePackageFiles(t, packageName) {
		for _, declaration := range parsed.file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.VAR {
				continue
			}
			for _, specification := range general.Specs {
				for _, name := range specificationNames(specification) {
					if ast.IsExported(name) && strings.HasPrefix(name, "Err") {
						names = append(names, name)
					}
				}
			}
		}
	}
	slices.Sort(names)
	return names
}

func TestFilterPublicFacadeKeepsFrontendInternalsPrivate(t *testing.T) {
	forbidden := map[string]bool{
		"Token": true, "Lexer": true, "Parser": true,
		"Analyzer": true, "Optimizer": true,
	}
	for name := range map[string]bool{
		"Analyze": true, "Optimize": true, "ParseAndAnalyze": true,
		"NewLexer": true, "NewParser": true, "NewAnalyzer": true, "NewOptimizer": true,
	} {
		forbidden[name] = true
	}
	assertTopLevelNamesAbsent(t, filepath.Join("vectorstore", "filter"), forbidden)

	for _, typ := range []reflect.Type{
		reflect.TypeFor[filter.Ident](),
		reflect.TypeFor[filter.Literal](),
		reflect.TypeFor[filter.ListLiteral](),
		reflect.TypeFor[filter.UnaryExpr](),
		reflect.TypeFor[filter.BinaryExpr](),
		reflect.TypeFor[filter.IndexExpr](),
	} {
		for field := range typ.Fields() {
			if containsInternalType(field.Type) {
				t.Errorf("public filter node %v field %s exposes internal type %v", typ, field.Name, field.Type)
			}
		}
	}
}

func containsInternalType(typ reflect.Type) bool {
	for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
		typ = typ.Elem()
	}
	return strings.Contains(typ.PkgPath(), "/internal/")
}

func assertSingleMethodInterface(t *testing.T, typ reflect.Type, method string) {
	t.Helper()
	if typ.Kind() != reflect.Interface || typ.NumMethod() != 1 || typ.Method(0).Name != method {
		t.Errorf("%v methods changed: want only %s", typ, method)
	}
}

func assertTopLevelNamesAbsent(t *testing.T, packagePath string, forbidden map[string]bool) {
	t.Helper()
	for _, parsed := range parsePackageFiles(t, packagePath) {
		for _, declaration := range parsed.file.Decls {
			for _, name := range topLevelNames(declaration) {
				if forbidden[name] {
					t.Errorf("core/%s must not reintroduce %s: %s", packagePath, name, parsed.path)
				}
			}
		}
	}
}

type parsedGoFile struct {
	path string
	file *ast.File
}

func parsePackageFiles(t *testing.T, packagePath string) []parsedGoFile {
	t.Helper()
	root := filepath.Join(coreRoot(t), packagePath)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	files := make([]parsedGoFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(root, entry.Name())
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		files = append(files, parsedGoFile{path: path, file: file})
	}
	return files
}

func topLevelNames(declaration ast.Decl) []string {
	switch declaration := declaration.(type) {
	case *ast.FuncDecl:
		if declaration.Recv == nil {
			return []string{declaration.Name.Name}
		}
	case *ast.GenDecl:
		var names []string
		for _, specification := range declaration.Specs {
			names = append(names, specificationNames(specification)...)
		}
		return names
	}
	return nil
}

func specificationNames(specification ast.Spec) []string {
	switch specification := specification.(type) {
	case *ast.TypeSpec:
		return []string{specification.Name.Name}
	case *ast.ValueSpec:
		names := make([]string, len(specification.Names))
		for index := range specification.Names {
			names[index] = specification.Names[index].Name
		}
		return names
	default:
		return nil
	}
}

func TestTargetChatSPIExcludesDefaultsAndIdentity(t *testing.T) {
	assertSingleMethodInterface(t, reflect.TypeFor[chat.Model](), "Call")
	assertSingleMethodInterface(t, reflect.TypeFor[chat.Streamer](), "Stream")
	assertTopLevelNamesAbsent(t, "chat", map[string]bool{"ModelMetadata": true})
}

func TestCoreDoesNotImportUpperScopeModules(t *testing.T) {
	const scopePrefix = "github.com/Tangerg/scope/"
	fset := token.NewFileSet()

	violations := 0
	for _, path := range productionGoFiles(t) {
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse imports in %s: %v", path, err)
		}
		for _, imp := range f.Imports {
			ip := strings.Trim(imp.Path.Value, `"`)
			rest, ok := strings.CutPrefix(ip, scopePrefix)
			if !ok {
				continue
			}
			if strings.HasPrefix(rest, "core/") || rest == "core" {
				continue
			}
			violations++
			rel, _ := filepath.Rel(coreRoot(t), path)
			t.Errorf("core must not import upper scope module %q: %s", ip, rel)
		}
	}
	if violations == 0 {
		t.Log("core import boundary holds: only core imports found")
	}
}

func productionGoFiles(t *testing.T) []string {
	t.Helper()
	root := coreRoot(t)
	var files []string
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == "vendor" || (strings.HasPrefix(name, ".") && path != root) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk core: %v", walkErr)
	}
	return files
}

func coreRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve architecture test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}
