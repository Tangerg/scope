package nomic

import "fmt"

type longTextMode string

const (
	longTextMean     longTextMode = "mean"
	longTextTruncate longTextMode = "truncate"
)

func (l longTextMode) validate() error {
	switch l {
	case longTextMean, longTextTruncate:
		return nil
	default:
		return fmt.Errorf("nomic: unsupported long_text_mode %q", l)
	}
}
