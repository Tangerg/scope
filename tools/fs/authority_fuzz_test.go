package fs

import (
	"path/filepath"
	"testing"
)

func FuzzLocalExecutorAuthorize(f *testing.F) {
	root := f.TempDir()
	executor := mustLocalExecutor(f, root)
	for _, seed := range []string{
		"",
		".",
		"..",
		"../outside",
		"nested/file.txt",
		"nested/../../outside",
		"~/secret",
		"name\x00with-nul",
		filepath.Join(root, "nested", "file.txt"),
		filepath.Join(filepath.Dir(root), "outside.txt"),
	} {
		f.Add(seed, false)
		f.Add(seed, true)
	}

	f.Fuzz(func(t *testing.T, untrusted string, allowRoot bool) {
		authorized, err := executor.authorize(untrusted, allowRoot)
		if err != nil {
			return
		}
		if authorized == "" || filepath.IsAbs(authorized) || !filepath.IsLocal(authorized) {
			t.Fatalf("authorize(%q) returned non-local path %q", untrusted, authorized)
		}
		if !allowRoot && authorized == "." {
			t.Fatalf("authorize(%q) granted the root for a file operation", untrusted)
		}

		joined := filepath.Join(executor.rootPath, authorized)
		relative, err := filepath.Rel(executor.rootPath, joined)
		if err != nil {
			t.Fatal(err)
		}
		if !filepath.IsLocal(relative) {
			t.Fatalf("authorize(%q) escaped %q as %q", untrusted, executor.rootPath, joined)
		}
	})
}
