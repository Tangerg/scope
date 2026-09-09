// Package core is the module overview for the Scope narrow waist. It declares
// no API of its own; every capability lives in a sibling package listed below.
//
// Core owns the serializable, provider-neutral protocols that every provider
// implements and every capability module consumes: the protocol values, the
// minimal calling SPI of each modality, and the pure composition around them.
// It owns no provider SDK, no network backend, no tokenizer vocabulary, no
// agent control flow, and no telemetry.
//
// # Protocol packages
//
// Each modality package owns one request/response protocol and the minimal
// [github.com/Tangerg/scope/core/chat.Model]-shaped SPI that carries it:
//
//   - chat: messages, parts, tool calls, usage, and output format.
//   - embedding: text-to-vector requests and vectors.
//   - image: image generation.
//   - moderation: content classification.
//   - rerank: query-to-document relevance ranking.
//   - speech: text-to-speech, with an independent Streamer.
//   - transcription: audio-to-text.
//
// Every one of them exports the same three sentinels — ErrInvalidOptions,
// ErrInvalidRequest, and ErrInvalidResponse — so providers and integrations
// classify failures identically. Chat extends that triple with its own protocol
// errors because its wire additionally models tool calls, parts, and usage.
// Classify with errors.Is; never match on message text.
// JSON decoders reject unknown struct members before replacing the receiver,
// including nested protocol values. Metadata, namespaced extensions, JSON
// schemas, and tool-defined payloads retain their open data contracts.
//
// # Shared values
//
// The document, media, and metadata packages own the values the protocols embed:
// the canonical Document, the Media container shared by every modality, and the
// JSON-safe typed extension values. A protocol DTO never carries a closure,
// reader, logger, tracer, registry, or native client.
//
// # Cross-protocol capabilities
//
// The jsonschema, tool, tokenizer, history, and vectorstore packages own the
// capabilities that are not specific to one modality: schema derivation and
// validation, the executable tool contract with its binding and authorization
// boundary, token counting and encoding, conversation history, and semantic
// indexing and search.
//
// # Direct clients
//
// The chatclient and embeddingclient packages add the ordinary-path
// conveniences — a middleware chain, prompt templates, and structured output —
// without changing the SPI. Option defaults stay with the provider that owns
// them. The chatclient/safeguard package screens input and output as
// fail-closed middleware.
//
// # Streaming
//
// A modality's Model has only Call. Real streaming is a separate Streamer, so a
// provider that cannot stream never has to pretend it can. Streams are
// iter.Seq2 values; early caller stop, context cancellation, and first-error
// termination are all part of the contract.
//
// # Reference implementations and contract suites
//
// The history/inmemory and vectorstore/inmemory packages are zero-dependency
// reference stores. The modeltest, history/storetest, and vectorstore/storetest
// packages hold the reusable conformance suites an implementor runs to be held
// to the same protocol as every other provider. They contain no provider
// implementation and are never a dependency of production code.
//
// # Boundaries
//
// Core never imports a sibling module, a provider SDK, a concrete tokenizer
// vocabulary, or OpenTelemetry. Instrumentation lives in the otel module and
// decorates Core from the outside. Retries, approvals, planning, and durable
// execution are Agent or Host concerns.
package core
