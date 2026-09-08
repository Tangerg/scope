package redis

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
)

// Index embeds documents and writes them as Redis HASHes keyed by
// `<KeyPrefix><id>`.
func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) (err error) {
	if validateErr := request.Validate(); validateErr != nil {
		return fmt.Errorf("redis.Store.Index: %w", validateErr)
	}

	var batches []*vectorstore.IndexRequest
	batches, err = request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return fmt.Errorf("redis: batch documents: %w", err)
	}

	for _, batch := range batches {
		docs := batch.Documents
		texts, err := batch.Texts()
		if err != nil {
			return fmt.Errorf("vectorstore: project document text: %w", err)
		}
		vectors, err := s.embeddingClient.EmbedTexts(ctx, texts)
		if err != nil {
			return fmt.Errorf("redis: embed documents: %w", err)
		}

		pipe := s.client.Pipeline()
		for i, doc := range docs {
			id := doc.ID
			metadataValues, valuesErr := doc.Metadata.Values()
			if valuesErr != nil {
				return fmt.Errorf("redis: decode metadata for %s: %w", id, valuesErr)
			}
			fields := map[string]any{
				s.contentField:   doc.Text,
				s.embeddingField: float32sToBytes(embedding.Float32Vector(vectors[i])),
			}
			for k, v := range metadataValues {
				field, formatErr := formatMetadataValue(v)
				if formatErr != nil {
					return fmt.Errorf("%w (document %s, key %s)", formatErr, id, k)
				}
				fields[k] = field
			}
			pipe.HSet(ctx, s.keyPrefix+id, fields)
		}

		if _, err = pipe.Exec(ctx); err != nil {
			return fmt.Errorf("redis: pipeline HSET: %w", err)
		}
	}
	return nil
}

// formatMetadataValue coerces a Go value into the HASH string form
// RediSearch can index. Slices and maps are JSON-encoded — they only
// matter when the caller stored them as TEXT fields.
//
// The composite branch reports a marshal failure instead of substituting
// fmt.Sprint. Go's %v rendering of a map or slice is not JSON, so the
// substitution wrote a value no reader can decode while telling the caller the
// document was stored as given.
func formatMetadataValue(v any) (any, error) {
	switch val := v.(type) {
	case nil:
		return "", nil
	case string, int, int64, float32, float64, bool:
		return val, nil
	case []byte:
		return val, nil
	default:
		b, err := json.Marshal(val)
		if err != nil {
			return nil, fmt.Errorf("redis: encode metadata value of type %T: %w", val, err)
		}
		return string(b), nil
	}
}
