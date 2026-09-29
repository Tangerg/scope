// Package tiktoken implements the core/tokenizer capabilities with OpenAI's
// tiktoken vocabularies from github.com/tiktoken-go/tokenizer.
//
// The vocabulary is chosen explicitly because no single encoding is correct
// across models:
//
//	tokenizer, err := tiktoken.New(ctx, tiktoken.O200KBase)
//	if err != nil {
//	    return err
//	}
//	count, err := tokenizer.CountText(ctx, "hello")
//
// An unknown encoding returns ErrInvalidEncoding instead of falling back to
// another vocabulary, because a wrong token count stays invisible until a
// request is rejected for length. Vocabularies are compiled into the
// dependency, so construction performs no network or filesystem I/O.
//
// Model-to-encoding routing and text splitting belong to callers and etl.
package tiktoken
