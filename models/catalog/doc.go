// Package catalog exposes the embedded model catalog: model identity, pricing,
// capabilities, modalities, and token limits. It is provider reference data,
// independent from the Core model invocation protocols.
//
// The catalog answers questions about a model — what it costs, which modalities
// it accepts, how many tokens it takes — without invoking anything. A caller
// that needs to call a model uses the matching models/<provider> module.
//
//	entry, found := catalog.Default.Lookup("openai", "gpt-4o")
//	if !found {
//	    return fmt.Errorf("unknown model")
//	}
//
// The data is embedded, so a lookup performs no I/O and cannot fail on a
// network. Updating it is a repository change rather than a runtime fetch.
//
// This module holds no provider SDK and implements no Core Model contract, and
// it never becomes a routing layer: choosing a model is the caller's decision
// and the catalog only supplies the facts.
//
// # Data
//
// One JSON file per provider under configs/, embedded with go:embed. Adding a
// provider is dropping a <provider>.json there; no code changes.
//
// The key rules a row has to satisfy:
//
//   - provider equals the adapter's Provider constant, lowercased, and Lookup
//     matches case-insensitively. An OpenAI-compatible provider delegates to
//     openai.NewChat but keeps its own Provider, so its rows are keyed by its
//     own name rather than by openai.
//   - pricing is an ascending array of rate bands in USD per million tokens.
//     A band reprices the whole prompt, not the marginal tokens, so a call
//     above a threshold bills entirely at that band. Omit pricing for a
//     metadata-only row.
//   - reasoning.supported is the authoritative "can reason" bit. levels and
//     default_level apply only where effort is level-controlled; a
//     token-budget reasoner is just supported.
//   - modalities list what the model accepts and emits. Only chat models are
//     included: an embedding, speech, or image model is filtered out.
//   - deprecated marks a retired model, which stays in the catalog so cost
//     still attributes for callers on the old id. A consumer hides or flags it.
//
// Rows are generated from two community model databases with disjoint
// responsibilities: models.dev decides which models exist and supplies every
// field except the reasoning effort ladder, and catwalk supplies that ladder.
// Regeneration is a repository change, which is why a lookup performs no I/O;
// scripts/update-model-catalog.sh performs it.
package catalog
