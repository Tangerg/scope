package anthropic

import "fmt"

type MessagesRevision string

const (
	MessagesRevision20230601 MessagesRevision = "2023-06-01"
	MessagesRevision20261021 MessagesRevision = "2026-10-21"
)

func (m MessagesRevision) Validate() error {
	switch m {
	case MessagesRevision20230601, MessagesRevision20261021:
		return nil
	default:
		return fmt.Errorf("anthropic: unsupported Messages revision %q", m)
	}
}
