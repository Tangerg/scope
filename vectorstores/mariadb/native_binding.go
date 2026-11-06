package mariadb

import (
	"fmt"

	"github.com/Tangerg/scope/core/vectorstore"
)

type nativeBinding struct {
	dimensions   int
	contentBytes int64
}

func (n nativeBinding) checkContent(text string) error {
	if int64(len(text)) > n.contentBytes {
		return fmt.Errorf("%w: mariadb content requires %d bytes; native column permits %d", vectorstore.ErrInvalidDocument, len(text), n.contentBytes)
	}
	return nil
}
