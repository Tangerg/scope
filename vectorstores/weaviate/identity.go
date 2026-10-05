package weaviate

import (
	"fmt"

	"github.com/google/uuid"
)

func validateObjectID(id string) error {
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id {
		return fmt.Errorf("%w %q: must be a lowercase hyphenated UUID", ErrInvalidObjectID, id)
	}
	return nil
}
