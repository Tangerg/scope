package textread

// These tests define the scanner's shared filesystem-adapter contract.

import (
	"context"
	"errors"
	"io"
	"math"
	"slices"
	"strings"
	"testing"
)

type typedNilReader struct{}

func (*typedNilReader) Read([]byte) (int, error) { return 0, io.EOF }

func TestScanValidatesUnselectedTailAndCountsTrailingLine(t *testing.T) {
	input := "\xef\xbb\xbffirst\r\nsecond\r\n"
	got, err := Scan(t.Context(), strings.NewReader(input), Options{
		InputBytes: int64(len(input)), LineBytes: 32, OutputBytes: 32, MaxLines: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "first" || got.StartLine != 0 || got.EndLine != 1 || got.TotalLines != 3 || !got.Truncated {
		t.Fatalf("Scan = %+v, want normalized first line and complete total", got)
	}

	invalid := "first\n" + string([]byte{0xff})
	_, err = Scan(t.Context(), strings.NewReader(invalid), Options{
		InputBytes: int64(len(invalid)), LineBytes: 32, OutputBytes: 32, MaxLines: 1,
	})
	if !errors.Is(err, ErrInvalidText) {
		t.Fatalf("invalid unselected tail error = %v, want ErrInvalidText", err)
	}
}

func TestScanEnforcesInputAndContextLimits(t *testing.T) {
	_, err := Scan(t.Context(), strings.NewReader("12345"), Options{
		InputBytes: 4, LineBytes: 8, OutputBytes: 8,
	})
	if !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("oversized input error = %v, want ErrInputTooLarge", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = Scan(ctx, strings.NewReader("text"), Options{
		InputBytes: 4, LineBytes: 4, OutputBytes: 4,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled scan error = %v, want context.Canceled", err)
	}
}

func TestScanPreservesReadFailureDuringCancellation(t *testing.T) {
	for _, operation := range []string{"scan", "visit"} {
		t.Run(operation, func(t *testing.T) {
			cause := errors.New("scan stopped")
			failure := errors.New("source read failed")
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			reader := cancelingReader{cancel: func() { cancel(cause) }, failure: failure}
			var err error
			if operation == "scan" {
				var result Result
				result, err = Scan(ctx, reader, Options{InputBytes: 16, LineBytes: 16, OutputBytes: 16})
				if result != (Result{}) {
					t.Errorf("failed Scan returned %+v", result)
				}
			} else {
				err = VisitLines(ctx, reader, Limits{InputBytes: 16, LineBytes: 16}, func(int, []byte) error {
					return nil
				})
			}
			if !errors.Is(err, cause) || !errors.Is(err, failure) {
				t.Errorf("scan error = %v, want cancellation and read failure", err)
			}
		})
	}
}

type cancelingReader struct {
	cancel  context.CancelFunc
	failure error
}

func (c cancelingReader) Read(buffer []byte) (int, error) {
	c.cancel()
	return copy(buffer, "text"), c.failure
}

func TestScanSupportsConsumerSpecificOutputBoundaries(t *testing.T) {
	input := "abcd\nefgh"
	complete, err := Scan(t.Context(), strings.NewReader(input), Options{
		InputBytes: int64(len(input)), LineBytes: 8, OutputBytes: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if complete.Content != "abcd" || complete.EndLine != 1 || !complete.Truncated {
		t.Fatalf("complete-line result = %+v", complete)
	}

	partial, err := Scan(t.Context(), strings.NewReader(input), Options{
		InputBytes: int64(len(input)), LineBytes: 8, OutputBytes: 7, PartialLine: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if partial.Content != "abcd\nef" || partial.EndLine != 2 || !partial.Truncated {
		t.Fatalf("partial-line result = %+v", partial)
	}
}

func TestVisitLinesSharesNormalizedBoundedValidation(t *testing.T) {
	var numbers []int
	var lines []string
	err := VisitLines(t.Context(), strings.NewReader("\xef\xbb\xbfneedle\r\nlast\r\n"), Limits{
		InputBytes: 32,
		LineBytes:  16,
	}, func(number int, line []byte) error {
		numbers = append(numbers, number)
		lines = append(lines, string(line))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(numbers, []int{1, 2, 3}) || !slices.Equal(lines, []string{"needle", "last", ""}) {
		t.Fatalf("VisitLines = %v %q, want normalized one-based lines", numbers, lines)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := VisitLines(ctx, strings.NewReader("text"), Limits{InputBytes: 4, LineBytes: 4}, func(int, []byte) error {
		return nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled VisitLines error = %v, want context.Canceled", err)
	}
}

func TestScanRejectsInvalidLimits(t *testing.T) {
	var typedNil *typedNilReader
	for _, test := range []struct {
		name    string
		source  io.Reader
		options Options
	}{
		{"typed nil source", typedNil, Options{InputBytes: 1, LineBytes: 1, OutputBytes: 1}},
		{"input limit overflow", strings.NewReader("x"), Options{InputBytes: math.MaxInt64, LineBytes: 1, OutputBytes: 1}},
		{"line limit overflow", strings.NewReader("x"), Options{InputBytes: 1, LineBytes: math.MaxInt, OutputBytes: 1}},
		{"negative max lines", strings.NewReader("x"), Options{InputBytes: 1, LineBytes: 1, OutputBytes: 1, MaxLines: -1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Scan(t.Context(), test.source, test.options); !errors.Is(err, ErrInvalidLimits) {
				t.Fatalf("Scan error = %v, want ErrInvalidLimits", err)
			}
		})
	}
}
