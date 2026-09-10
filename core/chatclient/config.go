package chatclient

import "github.com/Tangerg/scope/core/chat"

// Config supplies synchronous middleware. The first entry is the outermost
// wrapper. New consumes the slice during construction and does not retain it.
type Config struct {
	CallMiddleware []chat.CallMiddleware
}
