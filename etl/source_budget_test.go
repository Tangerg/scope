package etl_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Tangerg/scope/etl"
)

type nilSource struct{}

func (*nilSource) Read([]byte) (int, error) { return 0, nil }

func TestSourceBudgetRejectsOversizedInputWithoutReturningPartialData(t *testing.T) {
	budget, err := etl.NewSourceBudget(4)
	if err != nil {
		t.Fatal(err)
	}
	data, err := budget.ReadAll(t.Context(), strings.NewReader("12345"))
	if data != nil || !errors.Is(err, etl.ErrSourceTooLarge) {
		t.Fatalf("ReadAll = (%q, %v), want nil ErrSourceTooLarge", data, err)
	}
}

func TestSourceBudgetAcceptsExactBoundary(t *testing.T) {
	budget, err := etl.NewSourceBudget(4)
	if err != nil {
		t.Fatal(err)
	}
	data, err := budget.ReadAll(t.Context(), strings.NewReader("1234"))
	if err != nil || string(data) != "1234" {
		t.Fatalf("ReadAll = (%q, %v)", data, err)
	}
}

func TestSourceBudgetZeroValueUsesBoundedDefault(t *testing.T) {
	var budget etl.SourceBudget
	if budget.MaxBytes() != etl.DefaultMaxSourceBytes {
		t.Fatalf("MaxBytes = %d, want %d", budget.MaxBytes(), etl.DefaultMaxSourceBytes)
	}
}

func TestNewSourceBudgetRejectsNonPositiveValues(t *testing.T) {
	for _, maxBytes := range []int64{-1, 0} {
		if _, err := etl.NewSourceBudget(maxBytes); !errors.Is(err, etl.ErrInvalidSourceBudget) {
			t.Fatalf("NewSourceBudget(%d) error = %v", maxBytes, err)
		}
	}
}

func TestSourceBudgetHonorsCanceledContextBeforeReading(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	data, err := (etl.SourceBudget{}).ReadAll(ctx, strings.NewReader("unread"))
	if data != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadAll = (%q, %v), want nil context.Canceled", data, err)
	}
}

func TestSourceBudgetStopsBetweenReadsAndPreservesFailure(t *testing.T) {
	failure := errors.New("source read failed")
	for _, test := range []struct {
		name    string
		readErr error
	}{
		{name: "successful read"},
		{name: "failed read", readErr: failure},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			source := &cancelingSource{cancel: cancel, readErr: test.readErr}
			data, err := (etl.SourceBudget{}).ReadAll(ctx, source)
			if data != nil || !errors.Is(err, context.Canceled) {
				t.Errorf("ReadAll() = (%q, %v), want nil data and context.Canceled", data, err)
			}
			if test.readErr != nil && !errors.Is(err, test.readErr) {
				t.Errorf("ReadAll() error = %v, want original source failure", err)
			}
			if source.reads != 1 {
				t.Errorf("source reads = %d, want no reads after cancellation", source.reads)
			}
		})
	}
}

type cancelingSource struct {
	cancel  context.CancelFunc
	readErr error
	reads   int
}

func (c *cancelingSource) Read(buffer []byte) (int, error) {
	c.reads++
	if c.reads > 1 {
		return 0, io.EOF
	}
	c.cancel()
	return copy(buffer, "source"), c.readErr
}

func TestSourceBudgetRejectsNilSource(t *testing.T) {
	var typedNil *nilSource
	for _, source := range []io.Reader{nil, typedNil} {
		data, err := (etl.SourceBudget{}).ReadAll(t.Context(), source)
		if data != nil || !errors.Is(err, etl.ErrNilSource) {
			t.Fatalf("ReadAll = (%q, %v), want nil ErrNilSource", data, err)
		}
	}
}
