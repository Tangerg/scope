package parser

import (
	"encoding/ascii85"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestASCII85RequiresValidDataAndCompleteTerminator(t *testing.T) {
	for _, source := range []string{"!~", "!~x", "!!!!", "v~>"} {
		if _, err := io.ReadAll(ascii85.NewDecoder(newASCII85PayloadReader(strings.NewReader(source)))); err == nil {
			t.Fatalf("malformed ASCII85 accepted: %q", source)
		}
	}
	reader := newASCII85PayloadReader(strings.NewReader(" \tz\r\n~>"))
	if count, err := reader.Read(nil); count != 0 || err != nil {
		t.Fatalf("empty read=%d %v", count, err)
	}
	decoded, err := io.ReadAll(ascii85.NewDecoder(reader))
	if err != nil || string(decoded) != "\x00\x00\x00\x00" {
		t.Fatalf("whitespace or zero run=%x %v", decoded, err)
	}
	if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("reader after terminator=%v", err)
	}
}
