package fs

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func skipWithoutBash(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
}

func skipWithoutRipgrep(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep not available")
	}
}

func writeTemp(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestNewLocalExecutorRequiresExplicitRoot(t *testing.T) {
	if _, err := NewLocalExecutor(nil); !errors.Is(err, ErrInvalidRoot) {
		t.Fatalf("NewLocalExecutor(nil) error = %v, want ErrInvalidRoot", err)
	}
}

func TestLocalExecutor_Read_Whole(t *testing.T) {
	dir := t.TempDir()
	path := writeTemp(t, dir, "a.txt", "line1\nline2\nline3\n")
	out, err := mustLocalExecutor(t, dir).Read(t.Context(), ReadInput{Path: path})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if out.TotalLines != 4 {
		t.Errorf("TotalLines = %d, want 4", out.TotalLines)
	}
	if !strings.Contains(out.Content, "line1") || !strings.Contains(out.Content, "line3") {
		t.Errorf("Content = %q, missing expected lines", out.Content)
	}
}

func TestLocalExecutor_Read_LineRange(t *testing.T) {
	dir := t.TempDir()
	path := writeTemp(t, dir, "a.txt", "a\nb\nc\nd\ne\n")
	out, err := mustLocalExecutor(t, dir).Read(t.Context(), ReadInput{
		Path:   path,
		Offset: 1,
		Limit:  2,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if out.Content != "b\nc" {
		t.Errorf("Content = %q, want %q", out.Content, "b\nc")
	}
	if !out.Truncated {
		t.Error("Truncated = false, want true")
	}
}

func TestLocalExecutorRejectsInvalidOperationLimits(t *testing.T) {
	executor := mustLocalExecutor(t, t.TempDir())
	if _, err := executor.Read(t.Context(), ReadInput{Path: "x", Limit: -1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Read error = %v, want ErrInvalidInput", err)
	}
	if _, err := executor.Glob(t.Context(), GlobRequest{Pattern: "*", MaxResults: -1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Glob error = %v, want ErrInvalidInput", err)
	}
	if _, err := executor.Grep(t.Context(), GrepInput{Pattern: "x", BeforeContext: maximumContextLines + 1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Grep error = %v, want ErrInvalidInput", err)
	}
}

func TestLocalExecutor_Read_MaxBytes(t *testing.T) {
	dir := t.TempDir()
	path := writeTemp(t, dir, "a.txt", "ab你cd")
	out, err := mustLocalExecutor(t, dir).Read(t.Context(), ReadInput{
		Path: path, MaxOutputBytes: 4, PartialLine: true,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if out.Content != "ab" {
		t.Errorf("Content = %q, want %q", out.Content, "ab")
	}
	if !out.Truncated {
		t.Error("Truncated = false, want true")
	}
}

func TestLocalExecutor_Read_BinaryRejected(t *testing.T) {
	dir := t.TempDir()
	path := writeTemp(t, dir, "bin", "hello\x00world")
	_, err := mustLocalExecutor(t, dir).Read(t.Context(), ReadInput{Path: path})
	if !errors.Is(err, ErrBinaryFile) {
		t.Errorf("Read on binary: err = %v, want ErrBinaryFile", err)
	}
}

func TestLocalExecutor_Read_NormalizesCRLFAndBOM(t *testing.T) {
	dir := t.TempDir()
	path := writeTemp(t, dir, "a.txt", "\xEF\xBB\xBFa\r\nb\r\nc\r\n")
	out, err := mustLocalExecutor(t, dir).Read(t.Context(), ReadInput{Path: path})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if strings.Contains(out.Content, "\r") {
		t.Errorf("Content still contains \\r: %q", out.Content)
	}
	if strings.HasPrefix(out.Content, "\xEF\xBB\xBF") {
		t.Errorf("Content still has BOM prefix: %q", out.Content)
	}
}

func TestLocalExecutor_Write_Overwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new", "x.txt")
	out, err := mustLocalExecutor(t, dir).Write(t.Context(), WriteRequest{Path: path, Content: "hi"})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if out.BytesWritten != 2 {
		t.Errorf("BytesWritten = %d, want 2", out.BytesWritten)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "hi" {
		t.Errorf("file content = %q, want %q", got, "hi")
	}
}

func TestLocalExecutor_Write_PreservesCRLF(t *testing.T) {
	dir := t.TempDir()
	path := writeTemp(t, dir, "a.txt", "old\r\ncontent\r\n")
	_, err := mustLocalExecutor(t, dir).Write(t.Context(), WriteRequest{
		Path: path, Content: "new\nstuff\n",
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "\r\n") {
		t.Errorf("expected CRLF preserved, got %q", got)
	}
}

func TestLocalExecutor_Write_PreservesBOM(t *testing.T) {
	dir := t.TempDir()
	path := writeTemp(t, dir, "a.txt", "\xEF\xBB\xBFold")
	_, err := mustLocalExecutor(t, dir).Write(t.Context(), WriteRequest{
		Path: path, Content: "new",
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(got), "\xEF\xBB\xBF") {
		t.Errorf("expected BOM preserved, got %q", got)
	}
}

func TestLocalExecutorWriteDoesNotDuplicateExistingFormat(t *testing.T) {
	for _, content := range []string{"new\nstuff\n", "new\r\nstuff\r\n", utf8BOM + "new\r\nstuff\r\n"} {
		t.Run(content, func(t *testing.T) {
			dir := t.TempDir()
			path := writeTemp(t, dir, "a.txt", utf8BOM+"old\r\n")
			result, err := mustLocalExecutor(t, dir).Write(t.Context(), WriteRequest{Path: path, Content: content})
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want := utf8BOM + "new\r\nstuff\r\n"
			if string(data) != want || result.BytesWritten != len(want) {
				t.Fatalf("written = %q (%d bytes), want %q (%d bytes)", data, result.BytesWritten, want, len(want))
			}
		})
	}
}

func TestLocalExecutor_Write_NULRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	_, err := mustLocalExecutor(t, dir).Write(t.Context(), WriteRequest{
		Path: path, Content: "abc\x00def",
	})
	if !errors.Is(err, ErrBinaryFile) {
		t.Errorf("Write with NUL: err = %v, want ErrBinaryFile", err)
	}
}

func TestLocalExecutor_Write_ConcurrentSamePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "race.txt")
	exec := mustLocalExecutor(t, dir)
	const N = 32
	var wg sync.WaitGroup
	for i := range N {
		wg.Go(func() {
			content := strings.Repeat("x", 1024) + "\n"
			_, err := exec.Write(t.Context(), WriteRequest{Path: path, Content: content})
			if err != nil {
				t.Errorf("Write[%d]: %v", i, err)
			}
		})
	}
	wg.Wait()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	want := strings.Repeat("x", 1024) + "\n"
	if string(got) != want {
		t.Errorf("torn write detected: len=%d, want exactly %d", len(got), len(want))
	}
}

func TestLocalExecutor_Edit_SingleOccurrence(t *testing.T) {
	dir := t.TempDir()
	path := writeTemp(t, dir, "a.txt", "alpha beta gamma\n")
	out, err := mustLocalExecutor(t, dir).Edit(t.Context(), EditRequest{
		Path: path, OldString: "beta", NewString: "BETA",
	})
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if out.Replacements != 1 {
		t.Errorf("Replacements = %d, want 1", out.Replacements)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "alpha BETA gamma\n" {
		t.Errorf("content = %q", got)
	}
}

func TestLocalExecutor_Edit_MultipleOccurrencesRejected(t *testing.T) {
	dir := t.TempDir()
	path := writeTemp(t, dir, "a.txt", "x x x\n")
	_, err := mustLocalExecutor(t, dir).Edit(t.Context(), EditRequest{
		Path: path, OldString: "x", NewString: "y",
	})
	if err == nil {
		t.Fatal("Edit with non-unique match: want error")
	}
	if !strings.Contains(err.Error(), "matches 3 times") {
		t.Errorf("err = %v, want 'matches 3 times'", err)
	}
}

func TestLocalExecutor_Edit_ReplaceAll(t *testing.T) {
	dir := t.TempDir()
	path := writeTemp(t, dir, "a.txt", "x x x\n")
	out, err := mustLocalExecutor(t, dir).Edit(t.Context(), EditRequest{
		Path: path, OldString: "x", NewString: "y", ReplaceAll: true,
	})
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if out.Replacements != 3 {
		t.Errorf("Replacements = %d, want 3", out.Replacements)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "y y y\n" {
		t.Errorf("content = %q", got)
	}
}

func TestLocalExecutor_Edit_NoMatch(t *testing.T) {
	dir := t.TempDir()
	path := writeTemp(t, dir, "a.txt", "alpha\n")
	_, err := mustLocalExecutor(t, dir).Edit(t.Context(), EditRequest{
		Path: path, OldString: "beta", NewString: "BETA",
	})
	if err == nil {
		t.Fatal("Edit with no match: want error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v, want 'not found'", err)
	}
}

func TestLocalExecutor_Edit_PreservesCRLF(t *testing.T) {
	dir := t.TempDir()
	path := writeTemp(t, dir, "a.txt", "alpha\r\nbeta\r\n")
	_, err := mustLocalExecutor(t, dir).Edit(t.Context(), EditRequest{
		Path: path, OldString: "beta", NewString: "BETA",
	})
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "alpha\r\nBETA\r\n" {
		t.Errorf("content = %q, want CRLF preserved", got)
	}
}

func TestLocalExecutor_Edit_BinaryRejected(t *testing.T) {
	dir := t.TempDir()
	path := writeTemp(t, dir, "bin", "hello\x00")
	_, err := mustLocalExecutor(t, dir).Edit(t.Context(), EditRequest{
		Path: path, OldString: "hello", NewString: "hi",
	})
	if !errors.Is(err, ErrBinaryFile) {
		t.Errorf("Edit on binary: err = %v, want ErrBinaryFile", err)
	}
}

func TestLocalExecutor_ApplyPatch_ModifyCreateDelete(t *testing.T) {
	dir := t.TempDir()
	writeTemp(t, dir, "a.txt", "one\ntwo\nthree\n")
	writeTemp(t, dir, "gone.txt", "remove me\n")
	patch := `diff --git a/a.txt b/a.txt
--- a/a.txt
+++ b/a.txt
@@ -1,3 +1,3 @@
 one
-two
+TWO
 three
diff --git a/new.txt b/new.txt
--- /dev/null
+++ b/new.txt
@@ -0,0 +1,2 @@
+hello
+world
diff --git a/gone.txt b/gone.txt
--- a/gone.txt
+++ /dev/null
@@ -1 +0,0 @@
-remove me
`
	out, err := mustLocalExecutor(t, dir).ApplyPatch(t.Context(), ApplyPatchRequest{Patch: patch})
	if err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	if out.Hunks != 3 || len(out.Files) != 3 {
		t.Fatalf("ApplyPatch output = %+v, want 3 hunks / files", out)
	}
	a, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(a) != "one\nTWO\nthree\n" {
		t.Fatalf("a.txt = %q", a)
	}
	newFile, _ := os.ReadFile(filepath.Join(dir, "new.txt"))
	if string(newFile) != "hello\nworld\n" {
		t.Fatalf("new.txt = %q", newFile)
	}
	if _, err := os.Stat(filepath.Join(dir, "gone.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("gone.txt still exists: %v", err)
	}
}

func TestLocalExecutorApplyPatchGitHeadersNameRealPrefixDirectories(t *testing.T) {
	dir := t.TempDir()
	writeTemp(t, dir, "a/victim.txt", "old\n")
	writeTemp(t, dir, "victim.txt", "old\n")
	writeTemp(t, dir, "b/before.txt", "moved\n")
	writeTemp(t, dir, "a/empty", "")
	patch := "diff --git a/a/victim.txt b/a/victim.txt\n--- a/a/victim.txt\n+++ b/a/victim.txt\n@@ -1 +1 @@\n-old\n+new\n" +
		"diff --git a/a/created.txt b/a/created.txt\nnew file mode 100644\n--- /dev/null\n+++ b/a/created.txt\n@@ -0,0 +1 @@\n+created\n" +
		movePatch("b/before.txt", "b/after.txt", "") +
		"diff --git a/b/new-empty b/b/new-empty\nnew file mode 100644\nindex 0000000..e69de29\n" +
		"diff --git a/a/empty b/a/empty\ndeleted file mode 100644\nindex e69de29..0000000\n"
	out, err := mustLocalExecutor(t, dir).ApplyPatch(t.Context(), ApplyPatchRequest{Patch: patch})
	if err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	want := []PatchFileResponse{
		{Path: filepath.FromSlash("a/victim.txt"), Hunks: 1},
		{Path: filepath.FromSlash("a/created.txt"), Hunks: 1, Created: true},
		{Path: filepath.FromSlash("b/after.txt"), MovedFrom: filepath.FromSlash("b/before.txt")},
		{Path: filepath.FromSlash("b/new-empty"), Created: true},
		{Path: filepath.FromSlash("a/empty"), Deleted: true},
	}
	if !slices.Equal(out.Files, want) {
		t.Fatalf("ApplyPatch files = %+v, want %+v", out.Files, want)
	}
	for name, content := range map[string]string{
		"a/victim.txt": "new\n", "victim.txt": "old\n", "a/created.txt": "created\n", "b/after.txt": "moved\n", "b/new-empty": "",
	} {
		if got, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(got) != content {
			t.Fatalf("%s = %q, %v; want %q", name, got, err, content)
		}
	}
	for _, name := range []string{"created.txt", "b/before.txt", "a/empty"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s exists: %v", name, err)
		}
	}
}

func TestLocalExecutorApplyPatchRejectsNonEmptyDeleteWithoutHunks(t *testing.T) {
	dir := t.TempDir()
	writeTemp(t, dir, "kept", "content\n")
	patch := "diff --git a/kept b/kept\ndeleted file mode 100644\nindex e69de29..0000000\n"
	if _, err := mustLocalExecutor(t, dir).ApplyPatch(t.Context(), ApplyPatchRequest{Patch: patch}); err == nil {
		t.Fatal("ApplyPatch deleted a non-empty file without hunks")
	}
	if got, err := os.ReadFile(filepath.Join(dir, "kept")); err != nil || string(got) != "content\n" {
		t.Fatalf("kept = %q, %v", got, err)
	}
}

func TestLocalExecutor_ApplyPatch_MismatchLeavesFileUntouched(t *testing.T) {
	dir := t.TempDir()
	path := writeTemp(t, dir, "a.txt", "one\ntwo\n")
	patch := `--- a/a.txt
+++ b/a.txt
@@ -1,2 +1,2 @@
 one
-missing
+MISSING
`
	_, err := mustLocalExecutor(t, dir).ApplyPatch(t.Context(), ApplyPatchRequest{Patch: patch})
	if err == nil {
		t.Fatal("ApplyPatch mismatch: want error")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "one\ntwo\n" {
		t.Fatalf("content changed despite failed patch: %q", got)
	}
}

func TestLocalExecutor_ApplyPatch_SecondFileMismatchLeavesFirstUntouched(t *testing.T) {
	dir := t.TempDir()
	first := writeTemp(t, dir, "first.txt", "one\ntwo\n")
	second := writeTemp(t, dir, "second.txt", "alpha\nbeta\n")
	patch := `--- a/first.txt
+++ b/first.txt
@@ -1,2 +1,2 @@
 one
-two
+TWO
--- a/second.txt
+++ b/second.txt
@@ -1,2 +1,2 @@
 alpha
-missing
+MISSING
`
	_, err := mustLocalExecutor(t, dir).ApplyPatch(t.Context(), ApplyPatchRequest{Patch: patch})
	if err == nil {
		t.Fatal("ApplyPatch second-file mismatch: want error")
	}
	if got, _ := os.ReadFile(first); string(got) != "one\ntwo\n" {
		t.Fatalf("first file changed despite patch failure: %q", got)
	}
	if got, _ := os.ReadFile(second); string(got) != "alpha\nbeta\n" {
		t.Fatalf("second file changed despite patch failure: %q", got)
	}
}

func TestLocalExecutor_ApplyPatch_DuplicateFileRejectedLeavesFileUntouched(t *testing.T) {
	dir := t.TempDir()
	path := writeTemp(t, dir, "a.txt", "one\ntwo\nthree\n")
	patch := `--- a/a.txt
+++ b/a.txt
@@ -1,3 +1,3 @@
 one
-two
+TWO
 three
--- a/a.txt
+++ b/a.txt
@@ -1,3 +1,3 @@
 one
-two
+SECOND
 three
`
	_, err := mustLocalExecutor(t, dir).ApplyPatch(t.Context(), ApplyPatchRequest{Patch: patch})
	if err == nil {
		t.Fatal("ApplyPatch duplicate file section: want error")
	}
	if !strings.Contains(err.Error(), "duplicate file patch") {
		t.Fatalf("err = %v, want duplicate file patch", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "one\ntwo\nthree\n" {
		t.Fatalf("content changed despite duplicate file patch: %q", got)
	}
}

func TestLocalExecutor_ApplyPatch_InvalidRangeRejected(t *testing.T) {
	dir := t.TempDir()
	writeTemp(t, dir, "a.txt", "one\n")
	patch := `--- a/a.txt
+++ b/a.txt
@@ --1,1 +1,1 @@
-one
+ONE
`
	_, err := mustLocalExecutor(t, dir).ApplyPatch(t.Context(), ApplyPatchRequest{Patch: patch})
	if err == nil {
		t.Fatal("ApplyPatch invalid range: want error")
	}
	if !strings.Contains(err.Error(), "must not be negative") {
		t.Fatalf("err = %v, want negative hunk position", err)
	}
}

func TestLocalExecutor_Glob_BasicAndDoublestar(t *testing.T) {
	skipWithoutBash(t)
	dir := t.TempDir()
	writeTemp(t, dir, "a.go", "")
	writeTemp(t, dir, "sub/b.go", "")
	writeTemp(t, dir, "sub/nested/c.go", "")
	writeTemp(t, dir, "sub/d.txt", "")

	out, err := mustLocalExecutor(t, dir).Glob(t.Context(), GlobRequest{Pattern: "**/*.go"})
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(out.Paths) != 3 {
		t.Errorf("found %d paths, want 3: %v", len(out.Paths), out.Paths)
	}
}

func TestLocalExecutor_Glob_MaxResults(t *testing.T) {
	skipWithoutBash(t)
	dir := t.TempDir()
	for i := range 10 {
		writeTemp(t, dir, "file_"+string(rune('a'+i))+".txt", "")
	}
	out, err := mustLocalExecutor(t, dir).Glob(t.Context(), GlobRequest{
		Pattern:    "*.txt",
		MaxResults: 3,
	})
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(out.Paths) != 3 {
		t.Errorf("got %d paths, want 3 (capped)", len(out.Paths))
	}
	if !out.Truncated {
		t.Error("Truncated = false, want true")
	}
}

func TestLocalExecutor_Glob_UsesFullDoublestarSyntax(t *testing.T) {
	dir := t.TempDir()
	writeTemp(t, dir, "src/alpha/main.go", "")
	writeTemp(t, dir, "src/beta/nested/main.go", "")
	writeTemp(t, dir, "src/gamma/main.go", "")

	out, err := mustLocalExecutor(t, dir).Glob(t.Context(), GlobRequest{Pattern: "src/{alpha,beta}/**/main.go"})
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	want := []string{"src/alpha/main.go", "src/beta/nested/main.go"}
	if !slices.Equal(out.Paths, want) {
		t.Fatalf("paths = %v, want %v", out.Paths, want)
	}
}

func TestLocalExecutor_Glob_TreatsPathAsLiteralDirectory(t *testing.T) {
	dir := t.TempDir()
	writeTemp(t, dir, "src[1]/a.go", "")
	writeTemp(t, dir, "src1/b.go", "")
	writeTemp(t, dir, "src*/c.go", "")

	for _, testCase := range []struct{ path, want string }{
		{path: "src[1]", want: filepath.Join("src[1]", "a.go")},
		{path: "src*", want: filepath.Join("src*", "c.go")},
	} {
		out, err := mustLocalExecutor(t, dir).Glob(t.Context(), GlobRequest{Pattern: "*.go", Path: testCase.path})
		if err != nil {
			t.Fatalf("Glob(%q): %v", testCase.path, err)
		}
		if want := []string{testCase.want}; !slices.Equal(out.Paths, want) {
			t.Fatalf("Glob(%q) paths = %v, want %v", testCase.path, out.Paths, want)
		}
	}
}

func TestGrepOutputModeOwnsDefaultAndValidation(t *testing.T) {
	for _, mode := range []GrepOutputMode{"", GrepOutputContent, GrepOutputFilesWithMatches, GrepOutputCount} {
		if !mode.Valid() {
			t.Errorf("%q should be valid", mode)
		}
	}
	if normalized, err := (GrepOutputMode("")).Normalize(); err != nil || normalized != GrepOutputContent {
		t.Fatalf("zero mode normalized to %q, %v; want %q", normalized, err, GrepOutputContent)
	}
	if normalized, err := GrepOutputMode("bogus").Normalize(); normalized != "" || !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid mode normalized to %q, %v", normalized, err)
	}
}

func TestLocalExecutor_Grep_Content(t *testing.T) {
	skipWithoutRipgrep(t)
	dir := t.TempDir()
	writeTemp(t, dir, "a.txt", "foo bar\nbaz foo\nqux\n")
	writeTemp(t, dir, "b.txt", "no match here\n")
	out, err := mustLocalExecutor(t, dir).Grep(t.Context(), GrepInput{Pattern: "foo"})
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if len(out.Lines) != 2 {
		t.Errorf("got %d lines, want 2: %#v", len(out.Lines), out.Lines)
	}
	for _, line := range out.Lines {
		if line.Kind != GrepLineMatch {
			t.Errorf("line kind = %q, want match", line.Kind)
		}
	}
}

func TestLocalExecutor_Grep_FilesWithMatches(t *testing.T) {
	skipWithoutRipgrep(t)
	dir := t.TempDir()
	writeTemp(t, dir, "a.txt", "foo\n")
	writeTemp(t, dir, "b.txt", "foo\nfoo\n")
	writeTemp(t, dir, "c.txt", "nothing\n")
	out, err := mustLocalExecutor(t, dir).Grep(t.Context(), GrepInput{
		Pattern:    "foo",
		OutputMode: GrepOutputFilesWithMatches,
	})
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if len(out.Files) != 2 {
		t.Errorf("files = %d, want 2: %v", len(out.Files), out.Files)
	}
	if len(out.Lines) != 0 {
		t.Errorf("Lines populated in files mode: %v", out.Lines)
	}
}

func TestLocalExecutor_Grep_Count(t *testing.T) {
	skipWithoutRipgrep(t)
	dir := t.TempDir()
	writeTemp(t, dir, "a.txt", "foo\nbar\nfoo\n")
	writeTemp(t, dir, "b.txt", "foo\n")
	writeTemp(t, dir, "c.txt", "nothing\n")
	out, err := mustLocalExecutor(t, dir).Grep(t.Context(), GrepInput{
		Pattern:    "foo",
		OutputMode: GrepOutputCount,
	})
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if len(out.Counts) != 2 {
		t.Errorf("counts = %d, want 2 (zero-count file must be filtered): %v", len(out.Counts), out.Counts)
	}
	for _, c := range out.Counts {
		if c.Count == 0 {
			t.Errorf("count = 0 for %q, should have been filtered", c.Path)
		}
	}
}

func TestLocalExecutor_Grep_InvalidMode(t *testing.T) {
	dir := t.TempDir()
	_, err := mustLocalExecutor(t, dir).Grep(t.Context(), GrepInput{
		Pattern:    "foo",
		OutputMode: "bogus",
	})
	if err == nil {
		t.Fatal("Grep with bad output_mode: want error")
	}
}

func TestLocalExecutor_Grep_AsymmetricContext(t *testing.T) {
	skipWithoutRipgrep(t)
	dir := t.TempDir()
	writeTemp(t, dir, "context.txt", "before two\nbefore one\nmatch\nafter one\nafter two\n")
	executor := mustLocalExecutor(t, dir)
	for _, test := range []struct {
		name          string
		before, after int
		want          []GrepLine
	}{
		{"before only", 2, 0, []GrepLine{
			{Path: "context.txt", Line: 1, Text: "before two", Kind: GrepLineContext},
			{Path: "context.txt", Line: 2, Text: "before one", Kind: GrepLineContext},
			{Path: "context.txt", Line: 3, Text: "match", Kind: GrepLineMatch},
		}},
		{"after only", 0, 2, []GrepLine{
			{Path: "context.txt", Line: 3, Text: "match", Kind: GrepLineMatch},
			{Path: "context.txt", Line: 4, Text: "after one", Kind: GrepLineContext},
			{Path: "context.txt", Line: 5, Text: "after two", Kind: GrepLineContext},
		}},
		{"both", 1, 2, []GrepLine{
			{Path: "context.txt", Line: 2, Text: "before one", Kind: GrepLineContext},
			{Path: "context.txt", Line: 3, Text: "match", Kind: GrepLineMatch},
			{Path: "context.txt", Line: 4, Text: "after one", Kind: GrepLineContext},
			{Path: "context.txt", Line: 5, Text: "after two", Kind: GrepLineContext},
		}},
		{"neither", 0, 0, []GrepLine{
			{Path: "context.txt", Line: 3, Text: "match", Kind: GrepLineMatch},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			out, err := executor.Grep(t.Context(), GrepInput{
				Pattern: "match", BeforeContext: test.before, AfterContext: test.after,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(out.Lines, test.want) || out.Truncated {
				t.Fatalf("Grep = %#v, want %#v without truncation", out, test.want)
			}
		})
	}
}

func TestLocalExecutor_Grep_ReturnsStructuredContext(t *testing.T) {
	skipWithoutRipgrep(t)
	dir := t.TempDir()
	writeTemp(t, dir, "with:colon.txt", "before\nmatch here\nafter\n")
	out, err := mustLocalExecutor(t, dir).Grep(t.Context(), GrepInput{
		Pattern: "match", BeforeContext: 1, AfterContext: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Lines) != 3 {
		t.Fatalf("lines = %#v, want before/match/after", out.Lines)
	}
	wantKinds := []GrepLineKind{GrepLineContext, GrepLineMatch, GrepLineContext}
	for index, line := range out.Lines {
		if line.Kind != wantKinds[index] {
			t.Errorf("lines[%d].Kind = %q, want %q", index, line.Kind, wantKinds[index])
		}
		if !strings.Contains(line.Path, "with:colon.txt") {
			t.Errorf("lines[%d].Path = %q, want colon-bearing filename", index, line.Path)
		}
	}
}

func TestLocalExecutor_GrepRequiresRipgrep(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := mustLocalExecutor(t, t.TempDir()).Grep(t.Context(), GrepInput{Pattern: "match"})
	if !errors.Is(err, ErrRipgrepUnavailable) {
		t.Fatalf("Grep error = %v, want ErrRipgrepUnavailable", err)
	}
}
