package fs

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestGrepRejectsMalformedProtocolEvents(t *testing.T) {
	for _, test := range []struct {
		name  string
		event string
	}{
		{"invalid JSON", `{"type":`},
		{"unknown event", `{"type":"unexpected"}`},
		{"missing line number", `{"type":"match","data":{"lines":{"text":"value"}}}`},
		{"overflowing line number", fmt.Sprintf(`{"type":"match","data":{"line_number":%d,"lines":{"text":"value"}}}`, uint64(math.MaxInt)+1)},
		{"absent content", `{"type":"match","data":{"line_number":1}}`},
		{"competing content", `{"type":"match","data":{"line_number":1,"lines":{"text":"value","bytes":"dmFsdWU="}}}`},
		{"invalid base64", `{"type":"match","data":{"line_number":1,"lines":{"bytes":"%%%"}}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			decoder := newRipgrepDecoder(GrepOutputContent, 10, grepOutputBudget)
			decoder.sourcePath = "source.txt"
			if err := decoder.decode(strings.NewReader(test.event)); err == nil || decoder.response.Truncated || len(decoder.response.Lines) != 0 {
				t.Fatalf("malformed protocol became success or truncation: %+v, %v", decoder.response, err)
			}
		})
	}
}

func TestGrepRequiresSourceAttributionAndPreservesReadFailure(t *testing.T) {
	decoder := newRipgrepDecoder(GrepOutputContent, 10, grepOutputBudget)
	if err := decoder.decode(strings.NewReader(`{"type":"match","data":{"line_number":1,"lines":{"text":"value"}}}`)); err == nil {
		t.Fatal("unattributed match became a file result")
	}
	readErr := errors.New("ripgrep output interrupted")
	if err := decoder.decode(failedGrepReader{cause: readErr}); !errors.Is(err, readErr) || decoder.response.Truncated {
		t.Fatalf("read failure became successful truncation: %+v, %v", decoder.response, err)
	}
}

type failedGrepReader struct{ cause error }

func (f failedGrepReader) Read([]byte) (int, error) { return 0, f.cause }

func TestGrepOutputLimitOnlyTruncatesWhenAdditionalBytesExist(t *testing.T) {
	decoder := newRipgrepDecoder(GrepOutputContent, 10, 0)
	if err := decoder.decode(strings.NewReader("")); err != nil || decoder.response.Truncated {
		t.Fatalf("empty stream was truncated: %v", err)
	}
	if err := decoder.decode(strings.NewReader("x")); !errors.Is(err, errGrepOutputBudget) || !decoder.response.Truncated {
		t.Fatalf("excess byte was not bounded: %v", err)
	}
}
