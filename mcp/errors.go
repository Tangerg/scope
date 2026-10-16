package mcp

import "errors"

var (
	ErrIncompleteResult = errors.New("mcp: remote tool result requires further input")

	ErrNilServer = errors.New("mcp: server must not be nil")

	ErrNilSession = errors.New("mcp: session must not be nil")

	ErrMediaReference = errors.New("mcp: media references cannot cross MCP")

	ErrEmptyEmbeddedResource = errors.New("mcp: empty embedded resources are unsupported")
)
