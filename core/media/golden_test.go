package media_test

import (
	"bytes"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tangerg/scope/core/media"
)

func TestMediaGolden(t *testing.T) {
	inline, err := media.NewBytes("image/png", []byte{1, 2, 3, 255})
	if err != nil {
		t.Fatal(err)
	}
	inline.ID = "image-1"
	inline.Name = "pixel.png"
	if setErr := inline.Metadata.Set("width", 1); setErr != nil {
		t.Fatal(setErr)
	}
	uri, err := media.NewURI("application/pdf", "https://example.com/manual.pdf")
	if err != nil {
		t.Fatal(err)
	}
	reference, err := media.NewReference("audio/mpeg", "provider-file-1")
	if err != nil {
		t.Fatal(err)
	}

	fixture := struct {
		Bytes     *media.Media `json:"bytes"`
		URI       *media.Media `json:"uri"`
		Reference *media.Media `json:"reference"`
	}{Bytes: inline, URI: uri, Reference: reference}
	assertMediaGolden(t, "media.golden.json", fixture)
}

func assertMediaGolden(t *testing.T, name string, value any) {
	t.Helper()
	got, err := jsonv2.Marshal(value, jsonv2.Deterministic(true), jsontext.WithIndent("  "))
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	want, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("golden mismatch\n--- got ---\n%s--- want ---\n%s", got, want)
	}
}
