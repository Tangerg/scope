package vertexai

import (
	"context"
	"errors"
	"iter"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/google/internal/protocol"
)

// ChatConfig binds provider access and defaults shared by every chat call.
type ChatConfig struct {
	Client         ClientConfig
	DefaultOptions corechat.Options
}

func (c ChatConfig) Validate() error {
	return c.protocol().Validate()
}

func (c ChatConfig) protocol() protocol.ChatConfig {
	return protocol.ChatConfig{
		Provider:       protocolProvider,
		Client:         c.Client.protocol(),
		DefaultOptions: c.DefaultOptions,
	}
}

var (
	_ corechat.Model    = (*Chat)(nil)
	_ corechat.Streamer = (*Chat)(nil)
)

// Chat wraps this provider's protocol implementation so the wire type stays
// unexported. Callers depend on the Core modality contract, which lets the
// protocol change without breaking this module's public surface.
type Chat protocol.Chat

// NewChat rejects an invalid provider binding before the first chat call.
func NewChat(ctx context.Context, config ChatConfig) (*Chat, error) {
	adapter, err := protocol.NewChat(ctx, config.protocol())
	if err != nil {
		return nil, err
	}
	return (*Chat)(adapter), nil
}

func (c *Chat) Call(ctx context.Context, req *corechat.Request) (*corechat.Response, error) {
	if c == nil {
		return nil, errors.New("vertexai: nil Chat")
	}
	return (*protocol.Chat)(c).Call(ctx, req)
}

func (c *Chat) Stream(ctx context.Context, req *corechat.Request) iter.Seq2[*corechat.ResponseDelta, error] {
	if c == nil {
		return func(yield func(*corechat.ResponseDelta, error) bool) { yield(nil, errors.New("vertexai: nil Chat")) }
	}
	return (*protocol.Chat)(c).Stream(ctx, req)
}
