package voyage

import "fmt"

type embeddingInputType string

const (
	embeddingInputQuery    embeddingInputType = "query"
	embeddingInputDocument embeddingInputType = "document"
)

func (e embeddingInputType) validate() error {
	switch e {
	case embeddingInputQuery, embeddingInputDocument:
		return nil
	default:
		return fmt.Errorf("voyage: unsupported input_type %q", e)
	}
}
