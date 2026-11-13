// Package tokenizer defines small, provider-neutral capabilities for counting
// and encoding text tokens. [Limit] pairs a token maximum with the counter that
// measures it, and [BatchConfig] packs the exact rendered texts into stable,
// ordered groups under item and token budgets. Concrete vocabularies live in
// implementation packages such as github.com/Tangerg/scope/tokenizers/tiktoken.
package tokenizer
