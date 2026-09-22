package metadata_test

import (
	"bytes"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tangerg/scope/core/metadata"
)

func TestMapGolden(t *testing.T) {
	value := metadata.Map{}
	if err := value.Set("enabled", true); err != nil {
		t.Fatal(err)
	}
	if err := value.Set("labels", []string{"core", "protocol"}); err != nil {
		t.Fatal(err)
	}
	if err := value.Set("nested", map[string]int{"count": 2}); err != nil {
		t.Fatal(err)
	}
	assertMetadataGolden(t, "metadata.golden.json", value)
}

func assertMetadataGolden(t *testing.T, name string, value any) {
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
