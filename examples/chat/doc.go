// Command chat demonstrates an ordinary structured model call using only Core.
// The deterministic model keeps the example offline; it does not measure model
// quality. A provider's chat.Model can replace it without introducing an Engine.
//
// Run from the repository root:
//
//	go run ./examples/chat
//
// Or run from examples against the published Core version in go.mod:
//
//	GOWORK=off go run ./chat
//
// The checked README snippet comes from main.go. Consumer tests cover strict
// structured decoding, model errors, cancellation, and streaming completion,
// failure, and early stop. Streaming uses chatclient.NewStreamClient rather
// than requiring a model's synchronous capability. No Agent package enters
// either the command or its tests.
package main
