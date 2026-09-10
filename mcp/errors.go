package mcp

import "errors"

var (
	ErrNilServer = errors.New("mcp: server must not be nil")

	ErrNilSession = errors.New("mcp: session must not be nil")
)
