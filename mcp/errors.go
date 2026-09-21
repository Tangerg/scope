package mcp

import "errors"

var (
	ErrIncompleteResult = errors.New("mcp: remote tool result requires further input")

	ErrNilServer = errors.New("mcp: server must not be nil")

	ErrNilSession = errors.New("mcp: session must not be nil")
)
