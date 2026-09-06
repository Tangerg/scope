// Package otel is the module overview for Scope's OpenTelemetry integration. It
// declares no API of its own; every adapter lives in a subpackage listed below.
//
// The module adds traces and metrics to Core's capabilities from the outside
// and provides development exporters that write all three OTel signals to
// log/slog. Core itself never imports OpenTelemetry: a wrapper here is an
// ordinary decorator, and this module invents no tracer, meter, registry, or
// observation abstraction. The official API is the vendor-neutral layer.
//
// # Adapters
//
// Adapters are split by the contract they wrap, so one generic config never
// mixes different lifecycles:
//
//   - chat: chat call and lazy stream.
//   - embedding, image, moderation, rerank, speech, transcription: one modality each.
//   - eval: a generic Evaluator result.
//   - rag: retrieval.
//   - tool: tool invocation.
//   - history: history store and conversation listing.
//   - vectorstore: the vector-store capabilities.
//   - agent: managed Process activation, Step, and Effect, through an Observer
//     bound as an agent.EventListener.
//
// Chat streaming records gen_ai.client.operation.time_to_first_chunk and
// gen_ai.client.operation.time_per_output_chunk for received deltas, including
// metadata-only chunks. Token metrics retain known cumulative usage even when
// generation fails. Response model identity is recorded only when reported by
// the provider. Cache and reasoning token counts are subsets of the totals.
//
// The a2a and mcp modules are protocol integrations and use the official OTel
// API directly at their own call boundary, so this module has no adapter for
// them.
//
// # What never enters telemetry
//
// Prompts, queries, documents, media, audio, transcripts, and evaluation
// subjects stay out. An adapter records identity, counts, latency, outcome, and
// a stable low-cardinality error classification, because error.type is a metric
// dimension and a provider message would create one time series per string.
//
// # Development sinks
//
// The slog subpackage implements the three official SDK exporter interfaces,
// writing one slog record per span, metric batch, and log record. Export always
// returns nil so a sink failure never pollutes the business path, flushing is
// synchronous rather than batched, and an error span is promoted to error level.
// In production the composition root swaps these for the official OTLP
// exporters; the wrappers and the Core protocol code do not change.
package otel
