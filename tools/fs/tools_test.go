package fs

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	toolcontract "github.com/Tangerg/scope/core/tool"
)

type typedNilBackend struct{}

func (*typedNilBackend) Read(context.Context, ReadInput) (ReadOutput, error) {
	panic("typed nil backend was used")
}
func (*typedNilBackend) Write(context.Context, WriteRequest) (WriteResponse, error) {
	panic("typed nil backend was used")
}
func (*typedNilBackend) Edit(context.Context, EditRequest) (EditResponse, error) {
	panic("typed nil backend was used")
}
func (*typedNilBackend) ApplyPatch(context.Context, ApplyPatchRequest) (ApplyPatchResponse, error) {
	panic("typed nil backend was used")
}
func (*typedNilBackend) Glob(context.Context, GlobRequest) (GlobResponse, error) {
	panic("typed nil backend was used")
}
func (*typedNilBackend) Grep(context.Context, GrepInput) (GrepResponse, error) {
	panic("typed nil backend was used")
}

// Compile-time assertions that every tool constructor returns a value
// satisfying chat.Tool. (We re-assert here for documentation
// and to catch refactors that break the interface.)
func TestTools_Definitions(t *testing.T) {
	cases := []struct {
		name string
		got  string
	}{
		{"read", mustReadTool(t, mustLocalExecutor(t, ".")).Definition().Name},
		{"write", mustWriteTool(t, mustLocalExecutor(t, ".")).Definition().Name},
		{"edit", mustEditTool(t, mustLocalExecutor(t, ".")).Definition().Name},
		{"apply_patch", mustApplyPatchTool(t, mustLocalExecutor(t, ".")).Definition().Name},
		{"glob", mustGlobTool(t, mustLocalExecutor(t, ".")).Definition().Name},
		{"grep", mustGrepTool(t, mustLocalExecutor(t, ".")).Definition().Name},
	}
	for _, tc := range cases {
		if tc.got != tc.name {
			t.Errorf("tool %q has Definition().Name = %q", tc.name, tc.got)
		}
	}
}

func TestToolConstructorsRejectTypedNilExecutor(t *testing.T) {
	var backend *typedNilBackend
	for name, construct := range map[string]func() error{
		"read": func() error {
			_, err := NewReadTool(backend)
			return err
		},
		"write": func() error {
			_, err := NewWriteTool(backend)
			return err
		},
		"edit": func() error {
			_, err := NewEditTool(backend)
			return err
		},
		"apply_patch": func() error {
			_, err := NewApplyPatchTool(backend)
			return err
		},
		"glob": func() error {
			_, err := NewGlobTool(backend)
			return err
		},
		"grep": func() error {
			_, err := NewGrepTool(backend)
			return err
		},
	} {
		if err := construct(); !errors.Is(err, ErrNilExecutor) {
			t.Errorf("%s constructor error = %v, want ErrNilExecutor", name, err)
		}
	}
}

func TestToolContractsUseOneStrictFileVocabulary(t *testing.T) {
	for _, candidate := range []struct {
		name             string
		tool             toolcontract.Tool
		removedArguments string
	}{
		{"read", mustReadTool(t, mustLocalExecutor(t, ".")), `{"file_path":"old.txt"}`},
		{"write", mustWriteTool(t, mustLocalExecutor(t, ".")), `{"path":"new.txt","content":"x","append":true}`},
		{"edit", mustEditTool(t, mustLocalExecutor(t, ".")), `{"file_path":"old.txt","old_string":"a","new_string":"b"}`},
		{"apply_patch", mustApplyPatchTool(t, mustLocalExecutor(t, ".")), `{"patch":"x","unknown":true}`},
		{"glob", mustGlobTool(t, mustLocalExecutor(t, ".")), `{"pattern":"**/*","max_results":1001}`},
		{"grep", mustGrepTool(t, mustLocalExecutor(t, ".")), `{"pattern":"x","head_limit":1}`},
	} {
		t.Run(candidate.name, func(t *testing.T) {
			definition := candidate.tool.Definition()
			if strings.Contains(string(definition.InputSchema), `"file_path"`) {
				t.Fatalf("schema still exposes file_path: %s", definition.InputSchema)
			}
			if _, err := invokeTestTool(t.Context(), candidate.tool, candidate.removedArguments); err == nil {
				t.Fatalf("accepted removed or out-of-range arguments: %s", candidate.removedArguments)
			}
		})
	}
}

func TestGrepContractRejectsAmbiguousPagingAndContextFields(t *testing.T) {
	grep := mustGrepTool(t, mustLocalExecutor(t, "."))
	for _, arguments := range []string{
		`{"pattern":"x","glob":"**/*.go"}`,
		`{"pattern":"x","type":"go"}`,
		`{"pattern":"x","context":2}`,
		`{"pattern":"x","before_context":2}`,
		`{"pattern":"x","after_context":2}`,
		`{"pattern":"x","output_mode":"paths"}`,
		`{"pattern":"x","before_context_lines":21}`,
	} {
		if _, err := invokeTestTool(t.Context(), grep, arguments); err == nil {
			t.Fatalf("grep accepted invalid arguments: %s", arguments)
		}
	}
}

func TestReadTool_OneBasedStartLineTranslation(t *testing.T) {
	dir := t.TempDir()
	path := writeTemp(t, dir, "a.txt", "line1\nline2\nline3\nline4\n")

	tool := mustReadTool(t, mustLocalExecutor(t, dir))

	// start_line=2 means "start at line 2"; max_lines=2 takes line2,line3.
	output, err := invokeTestTool(t.Context(), tool, `{"path":"`+path+`","start_line":2,"max_lines":2}`)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	var resp ReadResponse
	if err := jsonv2.Unmarshal(output.Details, &resp); err != nil {
		t.Fatalf("Unmarshal: %v body=%s", err, output.Details)
	}
	if resp.Content != "line2\nline3" {
		t.Errorf("Content = %q, want %q", resp.Content, "line2\nline3")
	}
	if resp.StartLine != 2 {
		t.Errorf("StartLine = %d, want 2 (1-based)", resp.StartLine)
	}
	if resp.EndLine != 3 {
		t.Errorf("EndLine = %d, want 3 (1-based inclusive)", resp.EndLine)
	}
}

func TestReadTool_OmittedStartLineMeansFirstLine(t *testing.T) {
	dir := t.TempDir()
	path := writeTemp(t, dir, "a.txt", "a\nb\nc\n")
	output, err := invokeTestTool(t.Context(), mustReadTool(t, mustLocalExecutor(t, dir)), `{"path":"`+path+`","max_lines":1}`)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	var resp ReadResponse
	_ = jsonv2.Unmarshal(output.Details, &resp)
	if resp.StartLine != 1 {
		t.Errorf("StartLine = %d, want 1 when start_line is omitted", resp.StartLine)
	}
}

func TestReadTool_RejectsUnknownPagingArguments(t *testing.T) {
	for _, arguments := range []string{
		`{"path":"a.txt","offset":1}`,
		`{"path":"a.txt","limit":20}`,
		`{"path":"a.txt","start_line":0}`,
	} {
		if _, err := invokeTestTool(t.Context(), mustReadTool(t, mustLocalExecutor(t, ".")), arguments); err == nil {
			t.Fatalf("read accepted ambiguous paging arguments: %s", arguments)
		}
	}
}

func TestReadTool_EmptyPath(t *testing.T) {
	_, err := invokeTestTool(t.Context(), mustReadTool(t, mustLocalExecutor(t, ".")), `{"path":""}`)
	if err == nil {
		t.Fatal("Call with empty path: want error")
	}
}

func TestWriteTool_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	output, err := invokeTestTool(t.Context(), mustWriteTool(t, mustLocalExecutor(t, dir)), `{"path":"`+path+`","content":"hi"}`)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	var resp WriteResponse
	if err := jsonv2.Unmarshal(output.Details, &resp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if resp.BytesWritten != 2 {
		t.Errorf("BytesWritten = %d, want 2", resp.BytesWritten)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "hi" {
		t.Errorf("file = %q, want %q", got, "hi")
	}
}

func TestEditTool_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := writeTemp(t, dir, "a.txt", "alpha beta\n")
	output, err := invokeTestTool(t.Context(), mustEditTool(t, mustLocalExecutor(t, dir)),
		`{"path":"`+path+`","old_string":"beta","new_string":"BETA"}`)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	var resp EditResponse
	_ = jsonv2.Unmarshal(output.Details, &resp)
	if resp.Replacements != 1 {
		t.Errorf("Replacements = %d, want 1", resp.Replacements)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "alpha BETA\n" {
		t.Errorf("file = %q", got)
	}
}

func TestApplyPatchTool_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := writeTemp(t, dir, "a.txt", "alpha\nbeta\n")
	patch := `--- ` + path + `
+++ ` + path + `
@@ -1,2 +1,2 @@
 alpha
-beta
+BETA
`
	output, err := invokeTestTool(t.Context(), mustApplyPatchTool(t, mustLocalExecutor(t, dir)), string(mustJSON(t, ApplyPatchRequest{Patch: patch})))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	var resp ApplyPatchResponse
	if err := jsonv2.Unmarshal(output.Details, &resp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if resp.Hunks != 1 || len(resp.Files) != 1 {
		t.Fatalf("response = %+v, want one file/hunk", resp)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "alpha\nBETA\n" {
		t.Errorf("file = %q", got)
	}
}

func TestGrepTool_ContentMode(t *testing.T) {
	skipWithoutRipgrep(t)
	dir := t.TempDir()
	writeTemp(t, dir, "a.txt", "foo bar\n")
	output, err := invokeTestTool(t.Context(), mustGrepTool(t, mustLocalExecutor(t, dir)), `{"pattern":"foo"}`)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	var resp GrepResponse
	if err := jsonv2.Unmarshal(output.Details, &resp); err != nil {
		t.Fatalf("Unmarshal: %v body=%s", err, output.Details)
	}
	if len(resp.Lines) == 0 {
		t.Errorf("no matches in body=%s", output.Details)
	}
}

func TestGrepTool_FilesWithMatchesMode(t *testing.T) {
	skipWithoutRipgrep(t)
	dir := t.TempDir()
	writeTemp(t, dir, "a.txt", "foo\n")
	writeTemp(t, dir, "b.txt", "bar\n")
	output, err := invokeTestTool(t.Context(), mustGrepTool(t, mustLocalExecutor(t, dir)),
		`{"pattern":"foo","output_mode":"files_with_matches"}`)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	var resp GrepResponse
	if err := jsonv2.Unmarshal(output.Details, &resp); err != nil {
		t.Fatalf("Unmarshal: %v body=%s", err, output.Details)
	}
	if len(resp.Files) == 0 {
		t.Errorf("expected files populated; body=%s", output.Details)
	}
	if len(resp.Lines) != 0 {
		t.Errorf("lines must be empty in files mode: %v", resp.Lines)
	}
	// JSON sum-type sanity: lines/counts must be absent (omitempty)
	if strings.Contains(string(output.Details), `"lines"`) {
		t.Errorf("body should omit lines in files mode; got %s", output.Details)
	}
}

func TestGlobTool_Description(t *testing.T) {
	def := mustGlobTool(t, mustLocalExecutor(t, ".")).Definition()
	for _, kw := range []string{"**/*.go", "doublestar", "grep"} {
		if !strings.Contains(def.Description, kw) {
			t.Errorf("Description missing %q: %q", kw, def.Description)
		}
	}
}

func TestGrepTool_Description(t *testing.T) {
	def := mustGrepTool(t, mustLocalExecutor(t, ".")).Definition()
	for _, kw := range []string{"ripgrep", "multiline", "files_with_matches"} {
		if !strings.Contains(def.Description, kw) {
			t.Errorf("Description missing %q: %q", kw, def.Description)
		}
	}
}

func TestBadJSONArguments(t *testing.T) {
	tools := []struct {
		name string
		tool toolcontract.Tool
	}{
		{"read", mustReadTool(t, mustLocalExecutor(t, "."))},
		{"write", mustWriteTool(t, mustLocalExecutor(t, "."))},
		{"edit", mustEditTool(t, mustLocalExecutor(t, "."))},
		{"apply_patch", mustApplyPatchTool(t, mustLocalExecutor(t, "."))},
		{"glob", mustGlobTool(t, mustLocalExecutor(t, "."))},
		{"grep", mustGrepTool(t, mustLocalExecutor(t, "."))},
	}
	for _, tc := range tools {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := invokeTestTool(t.Context(), tc.tool, `{not json`); err == nil {
				t.Errorf("%s tool: want error on bad JSON", tc.name)
			}
		})
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := jsonv2.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return b
}
