package repoarch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
)

const repositoryModulePrefix = "github.com/Tangerg/scope"

type repositoryModule struct {
	path  string
	dir   string
	file  *modfile.File
	layer int
}

type providerPackage struct {
	family     string
	relative   string
	dir        string
	importPath string
}

func TestWorkspaceCoversEveryProductModule(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	modules := discoverModules(t, root)

	data, err := os.ReadFile(filepath.Join(root, "go.work"))
	if err != nil {
		t.Fatal(err)
	}
	work, err := modfile.ParseWork("go.work", data, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(work.Replace) != 0 {
		t.Error("go.work must not contain replace directives")
	}

	workspaceDirs := make(map[string]struct{}, len(work.Use))
	for _, use := range work.Use {
		dir := cleanWorkspacePath(use.Path)
		if _, duplicate := workspaceDirs[dir]; duplicate {
			t.Errorf("go.work lists %q more than once", dir)
		}
		workspaceDirs[dir] = struct{}{}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir), "go.mod")); err != nil {
			t.Errorf("go.work entry %q has no readable go.mod: %v", dir, err)
		}
	}

	for _, module := range modules {
		if _, ok := workspaceDirs[module.dir]; !ok {
			t.Errorf("module %s (%s) is missing from go.work", module.path, module.dir)
		}
		if module.file.Go == nil || work.Go == nil || module.file.Go.Version != work.Go.Version {
			moduleVersion := ""
			if module.file.Go != nil {
				moduleVersion = module.file.Go.Version
			}
			workspaceVersion := ""
			if work.Go != nil {
				workspaceVersion = work.Go.Version
			}
			t.Errorf("%s go version = %q; workspace = %q", module.path, moduleVersion, workspaceVersion)
		}
	}

	for dir := range workspaceDirs {
		if isExcludedAppDir(dir) {
			continue
		}
		if _, ok := moduleByDir(modules, dir); !ok {
			t.Errorf("go.work contains product module %q that repository discovery did not classify", dir)
		}
	}
}

func TestCoverageGatesTargetTheirOwningModules(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(workflow)
	if strings.Contains(text, "matrix.module == '.'") {
		t.Error("CI targets the retired root module; Core-only gates must select matrix.module == 'core'")
	}
	for gate, module := range map[string]string{
		"Core coverage budget":  "core",
		"Agent coverage budget": "agent",
	} {
		gateIndex := strings.Index(text, "- name: "+gate)
		if gateIndex < 0 {
			t.Errorf("CI is missing %q", gate)
			continue
		}
		nextStep := strings.Index(text[gateIndex+1:], "\n      - name:")
		block := text[gateIndex:]
		if nextStep >= 0 {
			block = text[gateIndex : gateIndex+1+nextStep]
		}
		if !strings.Contains(block, "if: matrix.module == '"+module+"'") {
			t.Errorf("CI gate %q does not select the %s module", gate, module)
		}
	}

	gateIndex := strings.Index(text, "- name: Provider coverage budget")
	if gateIndex < 0 {
		t.Fatal("CI is missing Provider coverage budget")
	}
	nextStep := strings.Index(text[gateIndex+1:], "\n      - name:")
	block := text[gateIndex:]
	if nextStep >= 0 {
		block = text[gateIndex : gateIndex+1+nextStep]
	}
	for _, family := range []string{"models/", "vectorstores/", "historystores/"} {
		if !strings.Contains(block, "startsWith(matrix.module, '"+family+"')") {
			t.Errorf("CI provider coverage gate does not select %s modules", family)
		}
	}
}

func TestProductModulesStayOutOfInternalDirectories(t *testing.T) {
	t.Parallel()
	for _, module := range discoverModules(t, repositoryRoot(t)) {
		if containsPathSegment(module.dir, "internal") {
			t.Errorf("module %s lives under internal; internal packages may hide implementation, but must not form dependency islands", module.path)
		}
	}
}

func TestProductModuleGraphFollowsLayeredOwnership(t *testing.T) {
	t.Parallel()
	modules := discoverModules(t, repositoryRoot(t))
	graph := make(map[string][]string, len(modules))

	for _, module := range modules {
		if len(module.file.Replace) != 0 {
			t.Errorf("%s uses replace; every product module must resolve outside go.work", module.path)
		}
		for _, requirement := range module.file.Require {
			dependency := requirement.Mod.Path
			if !isRepositoryImport(dependency) {
				continue
			}
			dependencyModule, ok := modules[dependency]
			if !ok {
				t.Errorf("%s requires undiscovered product module %s", module.path, dependency)
				continue
			}
			graph[module.path] = append(graph[module.path], dependency)
			if !allowedRepositoryDependency(module, dependencyModule) {
				t.Errorf("layer %d module %s must not depend on layer %d module %s", module.layer, module.path, dependencyModule.layer, dependency)
			}
		}
	}

	assertAcyclic(t, graph, modules)
}

func TestCoreModulePreservesDependencyDirection(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	modules := discoverModules(t, root)
	core, ok := modules[repositoryModulePrefix+"/core"]
	if !ok {
		t.Fatal("core module was not discovered")
	}
	err := filepath.WalkDir(filepath.Join(root, "core"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imported := range file.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				continue
			}
			if isRepositoryImport(importPath) && importPath != core.path && !strings.HasPrefix(importPath, core.path+"/") {
				t.Errorf("%s imports higher module %s", filepath.ToSlash(path), importPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestETLFilesystemAdaptersStayOutsideRoot(t *testing.T) {
	t.Parallel()
	paths, err := filepath.Glob(filepath.Join(repositoryRoot(t), "etl", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range file.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if importPath == "os" {
				t.Errorf("%s imports os; filesystem lifecycles belong in ETL format adapters", path)
			}
		}
	}
}

func TestDomainCapabilityModulesDoNotOwnOpenTelemetry(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	for _, relativeDir := range []string{
		"core", "agent", "etl", "eval", "rag", "skills", "tools",
	} {
		assertNoOpenTelemetryImports(t, filepath.Join(root, relativeDir))
	}
}

func assertNoOpenTelemetryImports(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imported := range file.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err == nil && strings.HasPrefix(importPath, "go.opentelemetry.io/otel") {
				t.Errorf("%s imports OpenTelemetry; domain instrumentation belongs in the otel module", filepath.ToSlash(path))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestProviderFamiliesUseLeafModules(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	for _, family := range []string{"historystores", "models", "tokenizers", "vectorstores"} {
		assertImmediateProviderModules(t, root, family, map[string]struct{}{"internal": {}, "protocol": {}}, true)
	}
	assertImmediateProviderModules(t, root, "etl", map[string]struct{}{
		"internal": {}, "json": {}, "markdown": {}, "text": {},
	}, false)
	assertImmediateProviderModules(t, root, "models/protocol", map[string]struct{}{"internal": {}}, true)
}

func TestWebProvidersShareToolsModule(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	err := filepath.WalkDir(filepath.Join(root, "tools", "web"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && entry.Name() == "go.mod" {
			t.Errorf("web provider %s must share the tools module", filepath.ToSlash(path))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestVectorAndHistoryStoreProvidersAreIndependentAndConformant(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	modules := discoverModules(t, root)

	for _, provider := range discoverVectorProviders(t, root) {
		assertProviderBoundary(t, provider)
		assertVectorConformance(t, provider)
		assertDependencyIsland(t, provider, modules)
	}
	for _, provider := range discoverHistoryStoreProviders(t, root) {
		assertProviderBoundary(t, provider)
		assertHistoryConformance(t, provider)
		assertDependencyIsland(t, provider, modules)
	}
}

func TestIntegrationFamiliesDoNotImportSiblingProviders(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	assertFamilySiblingBoundary(t, root, "models", map[string]struct{}{"catalog": {}, "protocol": {}})
	assertFamilySiblingBoundary(t, root, "tools/web", map[string]struct{}{"internal": {}})
	assertFamilySiblingBoundary(t, root, "etl", map[string]struct{}{
		"json": {}, "markdown": {}, "text": {},
	})
}

func TestPackageNamesDescribeTheirDirectories(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			if isExcludedAppDir(relative) || shouldSkipRepositoryDir(relative, entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.HasPrefix(string(data), "//go:build ignore\n") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, data, parser.PackageClauseOnly)
		if err != nil {
			return err
		}
		directory := filepath.ToSlash(filepath.Dir(relative))
		if file.Name.Name == "main" && containsPathSegment(directory, "examples") {
			return nil
		}
		want := filepath.Base(filepath.Dir(path))
		if file.Name.Name != want {
			t.Errorf("%s declares package %s; directory responsibility requires package %s", relative, file.Name.Name, want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRetiredLayoutsCannotReturn(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	for _, relative := range []string{
		"go.mod",
		"chatclient",
		"embeddingclient",
		"tool",
		"tokenizer",
		"tools/fakeweather",
		"tools/webfetch",
		"tools/websearch",
		"tools/httpreq/go.mod",
		"tools/skills/go.mod",
		"vectorstores/inmemory",
		"models/go.mod",
		"models/internal",
		"documentpipeline",
		"documentreaders",
		"etl/markdown/go.mod",
		"history",
		"evaluation",
		"evals",
		"rag/evaluation",
		"rag/eval",
		"otel/evaluation",
		"otel/evals",
		"internal/historykit",
		"internal/repoarch",
		"internal/vectorstorekit",
		"internal/vectorstorepg",
		"models/google/internal/options",
		"models/protocol/openai/internal/options",
		"tools/function",
		"tools/internal/schema",
		"vectorstores/cockroachdb",
		"vectorstores/pgvector",
		"vectorstores/storetest",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative))); err == nil {
			t.Errorf("retired layout %s has returned", relative)
		} else if !os.IsNotExist(err) {
			t.Errorf("inspect retired layout %s: %v", relative, err)
		}
	}
}

func discoverModules(t *testing.T, root string) map[string]repositoryModule {
	t.Helper()
	modules := make(map[string]repositoryModule)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			if isExcludedAppDir(relative) || shouldSkipRepositoryDir(relative, entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() != "go.mod" {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		parsed, err := modfile.Parse(path, data, nil)
		if err != nil {
			return err
		}
		if parsed.Module == nil {
			t.Errorf("%s has no module directive", path)
			return nil
		}
		relativeDir := filepath.ToSlash(filepath.Dir(relative))
		if relativeDir == "." {
			t.Errorf("repository root must be a workspace, not a Go module")
			return nil
		}
		wantPath := repositoryModulePrefix + "/" + relativeDir
		modulePath := parsed.Module.Mod.Path
		if modulePath != wantPath {
			t.Errorf("module path does not match directory: %s; want %s", modulePath, wantPath)
		}
		layer := moduleLayer(relativeDir)
		if layer < 0 {
			t.Errorf("module %s has no architecture layer", modulePath)
		}
		if previous, exists := modules[modulePath]; exists {
			t.Errorf("duplicate module path %s in %s and %s", modulePath, previous.dir, relativeDir)
		}
		modules[modulePath] = repositoryModule{path: modulePath, dir: relativeDir, file: parsed, layer: layer}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return modules
}

func moduleLayer(dir string) int {
	switch dir {
	case "core", "skills", "models/catalog":
		return 0
	case "a2a", "agent", "etl", "eval", "mcp", "rag", "tools":
		return 1
	case "otel":
		return 2
	case "examples":
		return 3
	case "dev/providerconformance", "dev/repoarch":
		return 4
	}
	if strings.HasPrefix(dir, "models/protocol/") {
		return 1
	}
	for _, prefix := range []string{
		"eval/",
		"etl/",
		"historystores/",
		"models/",
		"tokenizers/",
		"tools/",
		"vectorstores/",
	} {
		if strings.HasPrefix(dir, prefix) {
			return 2
		}
	}
	return -1
}

func allowedRepositoryDependency(source, target repositoryModule) bool {
	switch source.dir {
	case "core", "skills", "models/catalog", "dev/repoarch":
		return false
	case "examples":
		return target.layer < source.layer && !strings.HasPrefix(target.dir, "dev/")
	case "dev/providerconformance":
		return target.layer < source.layer && target.dir != "examples"
	}
	if target.dir == "core" {
		return true
	}
	if strings.HasPrefix(source.dir, "models/") && !strings.HasPrefix(source.dir, "models/protocol/") {
		return strings.HasPrefix(target.dir, "models/protocol/")
	}
	if source.dir == "tools" {
		return target.dir == "skills"
	}
	if strings.HasPrefix(source.dir, "etl/") {
		return target.dir == "etl"
	}
	if source.dir == "eval/trajectory" {
		return target.dir == "eval" || target.dir == "agent"
	}
	if source.dir == "otel" {
		return target.layer < source.layer && !strings.HasPrefix(target.dir, "dev/")
	}
	return false
}

func assertImmediateProviderModules(t *testing.T, root, family string, shared map[string]struct{}, all bool) {
	t.Helper()
	familyDir := filepath.Join(root, filepath.FromSlash(family))
	entries, err := os.ReadDir(familyDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, skip := shared[entry.Name()]; skip {
			continue
		}
		dir := filepath.Join(familyDir, entry.Name())
		if !hasAnyProductionGoFile(t, dir) {
			continue
		}
		if !all && !hasThirdPartyProductionImport(t, dir) {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
			t.Errorf("provider %s/%s must own a go.mod: %v", family, entry.Name(), err)
		}
	}
}

func discoverVectorProviders(t *testing.T, root string) []providerPackage {
	t.Helper()
	familyDir := filepath.Join(root, "vectorstores")
	entries, err := os.ReadDir(familyDir)
	if err != nil {
		t.Fatal(err)
	}
	var providers []providerPackage
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "internal" {
			continue
		}
		if entry.Name() != "postgres" {
			dir := filepath.Join(familyDir, entry.Name())
			if hasProductionGoFiles(t, dir) {
				providers = append(providers, newProvider("vectorstores", entry.Name(), dir))
			}
			continue
		}

		postgresEntries, err := os.ReadDir(filepath.Join(familyDir, "postgres"))
		if err != nil {
			t.Fatal(err)
		}
		for _, postgresEntry := range postgresEntries {
			if !postgresEntry.IsDir() || postgresEntry.Name() == "internal" {
				continue
			}
			relative := "postgres/" + postgresEntry.Name()
			dir := filepath.Join(familyDir, filepath.FromSlash(relative))
			if hasProductionGoFiles(t, dir) {
				providers = append(providers, newProvider("vectorstores", relative, dir))
			}
		}
	}
	slices.SortFunc(providers, func(a, b providerPackage) int { return strings.Compare(a.relative, b.relative) })
	return providers
}

func discoverHistoryStoreProviders(t *testing.T, root string) []providerPackage {
	t.Helper()
	familyDir := filepath.Join(root, "historystores")
	entries, err := os.ReadDir(familyDir)
	if err != nil {
		t.Fatal(err)
	}
	var providers []providerPackage
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "internal" {
			continue
		}
		dir := filepath.Join(familyDir, entry.Name())
		if hasProductionGoFiles(t, dir) {
			providers = append(providers, newProvider("historystores", entry.Name(), dir))
		}
	}
	slices.SortFunc(providers, func(a, b providerPackage) int { return strings.Compare(a.relative, b.relative) })
	return providers
}

func newProvider(family, relative, dir string) providerPackage {
	return providerPackage{
		family:     family,
		relative:   relative,
		dir:        dir,
		importPath: repositoryModulePrefix + "/" + family + "/" + relative,
	}
}

func assertProviderBoundary(t *testing.T, provider providerPackage) {
	t.Helper()
	entries, err := os.ReadDir(provider.dir)
	if err != nil {
		t.Error(err)
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" {
			continue
		}
		path := filepath.Join(provider.dir, entry.Name())
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Errorf("parse %s: %v", path, err)
			continue
		}
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				continue
			}
			if !strings.HasSuffix(path, "_test.go") && strings.HasPrefix(importPath, "go.opentelemetry.io/otel") {
				t.Errorf("%s imports OpenTelemetry; provider observability belongs in the otel module", filepath.ToSlash(path))
			}
			assertNoProviderSiblingImport(t, provider, path, importPath)
		}
	}
}

func assertNoProviderSiblingImport(t *testing.T, provider providerPackage, path, importPath string) {
	t.Helper()
	prefix := repositoryModulePrefix + "/" + provider.family + "/"
	if !strings.HasPrefix(importPath, prefix) {
		return
	}
	target := strings.TrimPrefix(importPath, prefix)
	if target == provider.relative || strings.HasPrefix(target, provider.relative+"/") {
		return
	}
	if provider.family == "vectorstores" && strings.HasPrefix(provider.relative, "postgres/") && strings.HasPrefix(target, "postgres/internal/") {
		return
	}
	t.Errorf("%s imports sibling provider %s", filepath.ToSlash(path), importPath)
}

func assertVectorConformance(t *testing.T, provider providerPackage) {
	t.Helper()
	assertConformanceCall(t, provider, "core/vectorstore/storetest")
}

func assertHistoryConformance(t *testing.T, provider providerPackage) {
	t.Helper()
	assertConformanceCall(t, provider, "core/history/storetest")
}

func assertConformanceCall(t *testing.T, provider providerPackage, suite string) {
	t.Helper()
	path := filepath.Join(provider.dir, "conformance_test.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Errorf("%s: missing or invalid conformance suite: %v", provider.importPath, err)
		return
	}

	wantImport := repositoryModulePrefix + "/" + suite
	aliases := make(map[string]struct{})
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil || importPath != wantImport {
			continue
		}
		alias := "storetest"
		if spec.Name != nil {
			alias = spec.Name.Name
		}
		aliases[alias] = struct{}{}
	}
	found := false
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Run" {
			return true
		}
		identifier, ok := selector.X.(*ast.Ident)
		if ok {
			_, found = aliases[identifier.Name]
		}
		return !found
	})
	if !found {
		t.Errorf("%s must call %s.Run", provider.importPath, suite)
	}
}

func assertDependencyIsland(t *testing.T, provider providerPackage, modules map[string]repositoryModule) {
	t.Helper()
	relative, err := filepath.Rel(repositoryRoot(t), provider.dir)
	if err != nil {
		t.Error(err)
		return
	}
	relative = filepath.ToSlash(relative)
	owner, ok := moduleForDirectory(modules, relative)
	if !ok {
		t.Errorf("%s has no module owner", provider.importPath)
		return
	}
	wantDir := provider.family + "/" + provider.relative
	if provider.family == "vectorstores" && strings.HasPrefix(provider.relative, "postgres/") {
		wantDir = "vectorstores/postgres"
	}
	if owner.dir != wantDir {
		t.Errorf("%s is owned by module %s; want %s", provider.importPath, owner.dir, wantDir)
	}
}

func assertFamilySiblingBoundary(t *testing.T, root, family string, shared map[string]struct{}) {
	t.Helper()
	familyDir := filepath.Join(root, filepath.FromSlash(family))
	err := filepath.WalkDir(familyDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" {
			return nil
		}
		relative, err := filepath.Rel(familyDir, path)
		if err != nil {
			return err
		}
		parts := strings.Split(filepath.ToSlash(relative), "/")
		if len(parts) < 2 {
			return nil
		}
		source := parts[0]
		if _, isShared := shared[source]; isShared {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		prefix := repositoryModulePrefix + "/" + family + "/"
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil || !strings.HasPrefix(importPath, prefix) {
				continue
			}
			target := firstPathSegment(strings.TrimPrefix(importPath, prefix))
			if target == source {
				continue
			}
			if _, isShared := shared[target]; isShared {
				continue
			}
			t.Errorf("%s imports sibling provider %s", filepath.ToSlash(path), importPath)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func hasProductionGoFiles(t *testing.T, dir string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".go" && !strings.HasSuffix(entry.Name(), "_test.go") {
			return true
		}
	}
	return false
}

func hasAnyProductionGoFile(t *testing.T, dir string) bool {
	t.Helper()
	found := false
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && filepath.Ext(path) == ".go" && !strings.HasSuffix(path, "_test.go") {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func hasThirdPartyProductionImport(t *testing.T, dir string) bool {
	t.Helper()
	found := false
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imported := range file.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err == nil && !isRepositoryImport(importPath) && isThirdPartyImport(importPath) {
				found = true
				return fs.SkipAll
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func moduleForDirectory(modules map[string]repositoryModule, dir string) (repositoryModule, bool) {
	var best repositoryModule
	found := false
	for _, module := range modules {
		if dir != module.dir && !strings.HasPrefix(dir, module.dir+"/") {
			continue
		}
		if !found || len(module.dir) > len(best.dir) {
			best, found = module, true
		}
	}
	return best, found
}

func moduleByDir(modules map[string]repositoryModule, dir string) (repositoryModule, bool) {
	for _, module := range modules {
		if module.dir == dir {
			return module, true
		}
	}
	return repositoryModule{}, false
}

func assertAcyclic(t *testing.T, graph map[string][]string, modules map[string]repositoryModule) {
	t.Helper()
	state := make(map[string]uint8, len(modules))
	var stack []string
	var visit func(string)
	visit = func(module string) {
		switch state[module] {
		case 1:
			start := slices.Index(stack, module)
			cycle := append(slices.Clone(stack[start:]), module)
			t.Errorf("module dependency cycle: %s", strings.Join(cycle, " -> "))
			return
		case 2:
			return
		}
		state[module] = 1
		stack = append(stack, module)
		for _, dependency := range graph[module] {
			visit(dependency)
		}
		stack = stack[:len(stack)-1]
		state[module] = 2
	}
	for module := range modules {
		visit(module)
	}
}

func cleanWorkspacePath(path string) string {
	cleaned := filepath.ToSlash(filepath.Clean(path))
	if cleaned == "" {
		return "."
	}
	return strings.TrimPrefix(cleaned, "./")
}

func containsPathSegment(path, want string) bool {
	return slices.Contains(strings.Split(filepath.ToSlash(path), "/"), want)
}

func firstPathSegment(path string) string {
	path = strings.TrimPrefix(filepath.ToSlash(path), "./")
	segment, _, _ := strings.Cut(path, "/")
	return segment
}

func isRepositoryImport(path string) bool {
	return path == repositoryModulePrefix || strings.HasPrefix(path, repositoryModulePrefix+"/")
}

func isThirdPartyImport(path string) bool {
	return strings.Contains(firstPathSegment(path), ".")
}

func isExcludedAppDir(relative string) bool {
	return relative == "app" || strings.HasPrefix(relative, "app/")
}

func shouldSkipRepositoryDir(relative, name string) bool {
	if relative == "." {
		return false
	}
	return strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor"
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve architecture test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}
