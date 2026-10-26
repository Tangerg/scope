package fs

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGrepCanSerializeMatchesContainingNonUTF8Bytes(t *testing.T) {
	skipWithoutRipgrep(t)
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "legacy.txt"), []byte("needle \xff\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	executor, err := NewLocalExecutor(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = executor.Close() })
	response, err := executor.Grep(t.Context(), GrepRequest{Pattern: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Lines) != 1 {
		t.Fatalf("matches = %v", response.Lines)
	}
	encoded, err := jsonv2.Marshal(response)
	if err != nil {
		t.Fatalf("matched bytes cannot reach the tool result: %v", err)
	}
	if strings.Contains(string(encoded), "\ufffd") {
		t.Fatalf("matched bytes were silently replaced: %s", encoded)
	}
	var decoded GrepResponse
	if err := jsonv2.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Lines) != 1 || !bytes.Equal(decoded.Lines[0].Content.Bytes(), []byte("needle \xff")) {
		t.Fatalf("round trip changed matched bytes: %s", encoded)
	}
}

func TestExactGrepWireBudgetDoesNotClaimTruncation(t *testing.T) {
	const event = `{"type":"match","data":{"lines":{"text":"needle\n"},"line_number":1}}`
	decoder := newRipgrepDecoder(GrepOutputContent, 1, len(event))
	decoder.sourcePath = "file.txt"
	if err := decoder.decode(strings.NewReader(event)); err != nil || decoder.response.Truncated || len(decoder.response.Lines) != 1 {
		t.Fatalf("exact budget: %v, %v", decoder.response, err)
	}
}

func TestGrepPreservesCarriageReturnsOutsideCRLF(t *testing.T) {
	for _, test := range []struct{ wire, want string }{
		{`"needle\r"`, "needle\r"},
		{`"needle\r\n"`, "needle"},
		{`"needle\r\r\n"`, "needle\r"},
	} {
		t.Run(test.wire, func(t *testing.T) {
			decoder := newRipgrepDecoder(GrepOutputContent, 1, grepOutputBudget)
			decoder.sourcePath = "source.txt"
			wire := `{"type":"match","data":{"line_number":1,"lines":{"text":` + test.wire + `}}}`
			if err := decoder.decode(strings.NewReader(wire)); err != nil {
				t.Fatal(err)
			}
			if len(decoder.response.Lines) != 1 || string(decoder.response.Lines[0].Content.Bytes()) != test.want {
				t.Fatalf("line bytes changed: %+v, want %q", decoder.response, test.want)
			}
		})
	}
}
