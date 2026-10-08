package mcp

import "errors"

var (
	ErrIncompleteResult = errors.New("mcp: remote tool result requires further input")

	ErrNilServer = errors.New("mcp: server must not be nil")

	ErrNilSession = errors.New("mcp: session must not be nil")

	// ErrMediaReference rejects a Core media reference, an opaque handle valid
	// only with the provider that issued it, which MCP has no content to carry.
	ErrMediaReference = errors.New("mcp: media references cannot cross MCP")
)
