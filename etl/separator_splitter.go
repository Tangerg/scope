package etl

import (
	"cmp"
	"context"
	"strings"
)

const defaultSeparator = "\n"

// SeparatorSplitterConfig configures fixed-separator chunking. The zero
// Separator uses a newline.
type SeparatorSplitterConfig struct {
	Separator string
}

// SeparatorSplitter is the TextSplitter that breaks text on a fixed separator.
type SeparatorSplitter struct {
	separator string
}

func NewSeparatorSplitter(config SeparatorSplitterConfig) (*SeparatorSplitter, error) {
	separator := cmp.Or(config.Separator, defaultSeparator)
	if err := validateTextEncoding(separator); err != nil {
		return nil, err
	}
	return &SeparatorSplitter{separator: separator}, nil
}

func (s *SeparatorSplitter) SplitText(ctx context.Context, text string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateTextEncoding(text); err != nil {
		return nil, err
	}
	return strings.Split(text, s.separator), nil
}
