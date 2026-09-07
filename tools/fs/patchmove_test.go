package fs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Moving a file with apply_patch uses Git's rename metadata. What makes it safe
// is that a destination is never overwritten, the origin is reported, and both
// endpoints are locked during the operation.

func movePatch(from, to, body string) string {
	patch := fmt.Sprintf("diff --git a/%s b/%s\nsimilarity index 100%%\nrename from %s\nrename to %s\n", from, to, from, to)
	if body != "" {
		patch += "--- a/" + from + "\n+++ b/" + to + "\n" + body
	}
	return patch
}

func TestApplyPatch_MoveCarriesContentAndRemovesTheOrigin(t *testing.T) {
	dir := t.TempDir()
	from := writeTemp(t, dir, "old.txt", "alpha\nbeta\n")
	to := filepath.Join(dir, "new.txt")

	out, err := mustLocalExecutor(t, dir).ApplyPatch(t.Context(), ApplyPatchRequest{
		Patch: movePatch("old.txt", "new.txt", "@@ -1,2 +1,2 @@\n alpha\n-beta\n+BETA\n"),
	})
	if err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}

	if _, statErr := os.Stat(from); !os.IsNotExist(statErr) {
		t.Errorf("origin still exists (err = %v)", statErr)
	}
	landed, err := os.ReadFile(to)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(landed) != "alpha\nBETA\n" {
		t.Errorf("destination = %q, want the patched content", landed)
	}
	if len(out.Files) != 1 || out.Files[0].MovedFrom != "old.txt" || out.Files[0].Path != "new.txt" {
		t.Errorf("output = %+v, want one file moved old.txt → new.txt", out.Files)
	}
}

func TestApplyPatch_PureRenameNeedsNoHunk(t *testing.T) {
	// git emits a rename with two headers and nothing to apply. Requiring a hunk
	// would make the one patch that changes no content impossible to express.
	dir := t.TempDir()
	from := writeTemp(t, dir, "old.txt", "unchanged\n")
	to := filepath.Join(dir, "renamed.txt")

	out, err := mustLocalExecutor(t, dir).ApplyPatch(t.Context(), ApplyPatchRequest{
		Patch: movePatch("old.txt", "renamed.txt", ""),
	})
	if err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	if out.Hunks != 0 {
		t.Errorf("hunks = %d, want 0", out.Hunks)
	}
	landed, err := os.ReadFile(to)
	if err != nil || string(landed) != "unchanged\n" {
		t.Errorf("destination = %q (err %v), want the original content", landed, err)
	}
	if _, err := os.Stat(from); !os.IsNotExist(err) {
		t.Errorf("origin still exists (err = %v)", err)
	}
}

func TestApplyPatch_MoveRefusesToOverwriteItsDestination(t *testing.T) {
	// The one outcome a rename must never produce. Without this the model's
	// mistaken destination silently replaces a file nobody asked about.
	dir := t.TempDir()
	from := writeTemp(t, dir, "old.txt", "moving\n")
	occupied := writeTemp(t, dir, "taken.txt", "do not lose me\n")

	_, err := mustLocalExecutor(t, dir).ApplyPatch(t.Context(), ApplyPatchRequest{
		Patch: movePatch("old.txt", "taken.txt", ""),
	})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %v, want it to name the collision", err)
	}
	kept, _ := os.ReadFile(occupied)
	if string(kept) != "do not lose me\n" {
		t.Errorf("destination = %q — the refusal did not protect it", kept)
	}
	if _, err := os.Stat(from); err != nil {
		t.Errorf("origin was removed by a refused patch: %v", err)
	}
}

func TestApplyPatch_RefusesTwoPatchesTouchingOneEndpoint(t *testing.T) {
	// Editing a file and moving another one onto it are two edits to one path, and
	// the result would depend on which was applied first.
	dir := t.TempDir()
	writeTemp(t, dir, "target.txt", "one\n")
	writeTemp(t, dir, "moving.txt", "two\n")

	_, err := mustLocalExecutor(t, dir).ApplyPatch(t.Context(), ApplyPatchRequest{
		Patch: "--- target.txt\n+++ target.txt\n@@ -1 +1 @@\n-one\n+ONE\n" +
			movePatch("moving.txt", "target.txt", ""),
	})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "duplicate file patch") {
		t.Errorf("error = %v, want it to name the duplicate", err)
	}
}

func TestApplyPatchRejectsAbsoluteAndRelativeAliasesBeforeMutation(t *testing.T) {
	dir := t.TempDir()
	path := writeTemp(t, dir, "file.txt", "one\ntwo\n")
	patch := fmt.Sprintf("--- a/file.txt\n+++ b/file.txt\n@@ -1,2 +1,2 @@\n-one\n+ONE\n two\n"+
		"--- %s\n+++ %s\n@@ -1,2 +1,2 @@\n one\n-two\n+TWO\n", path, path)
	response, err := mustLocalExecutor(t, dir).ApplyPatch(t.Context(), ApplyPatchRequest{Patch: patch})
	if err == nil || !strings.Contains(err.Error(), "duplicate file patch for file.txt") || len(response.Files) != 0 {
		t.Fatalf("response=%+v error=%v, want duplicate target rejection", response, err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "one\ntwo\n" {
		t.Fatalf("content=%q error=%v, want unchanged source", content, err)
	}
}

func TestApplyPatchReportsResolvedPaths(t *testing.T) {
	dir := t.TempDir()
	path := writeTemp(t, dir, "file.txt", "one\n")
	patch := fmt.Sprintf("--- %s\n+++ %s\n@@ -1 +1 @@\n-one\n+ONE\n", path, path)
	response, err := mustLocalExecutor(t, dir).ApplyPatch(t.Context(), ApplyPatchRequest{Patch: patch})
	if err != nil || len(response.Files) != 1 || response.Files[0].Path != "file.txt" || response.Hunks != 1 {
		t.Fatalf("response=%+v error=%v, want one root-relative target", response, err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "ONE\n" {
		t.Fatalf("content=%q error=%v, want committed content", content, err)
	}
}
