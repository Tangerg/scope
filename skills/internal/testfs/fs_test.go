package testfs_test

import (
	"io"
	"io/fs"
	"testing"

	"github.com/Tangerg/scope/skills/internal/testfs"
)

func TestResourceOpeningCannotEscapeTheFixtureDirectory(t *testing.T) {
	files := testfs.FS{
		"guide/note":        {Data: []byte("inside")},
		"other/note":        {Data: []byte("outside")},
		"guide/link":        {Data: []byte("../other"), Mode: fs.ModeSymlink},
		"guide/link/note":   {Data: []byte("must not traverse a link")},
		"guide/direct-link": {Data: []byte("../other/note"), Mode: fs.ModeSymlink},
	}
	for _, name := range []string{"../other/note", "/other/note", "link/note", "direct-link"} {
		if file, err := files.OpenInDir("guide", name); file != nil || err == nil {
			t.Fatalf("OpenInDir(%q) = %v, %v", name, file, err)
		}
	}
	file, err := files.OpenInDir("guide", "note")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil || string(data) != "inside" {
		t.Fatalf("resource = %q, %v", data, err)
	}
}
