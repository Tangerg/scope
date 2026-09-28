// Package tiktoken implements the core/tokenizer capabilities with OpenAI's
// tiktoken vocabularies, adapting github.com/tiktoken-go/tokenizer to the small
// interfaces Core defines.
//
// The vocabulary is chosen explicitly, because no single encoding is correct
// across models:
//
//	tokenizer, err := tiktoken.New(ctx, tiktoken.O200KBase)
//	if err != nil {
//	    return err
//	}
//
//	count, err := tokenizer.CountText(ctx, "hello")
//
// An unknown encoding returns ErrInvalidEncoding at construction rather than
// silently falling back to another vocabulary, because a wrong token count is
// not visible until a request is rejected for length.
//
// Vocabularies are compiled into the dependency. Validation and construction
// perform no network or filesystem I/O. Construction honors cancellation before
// and after bounded vocabulary initialization.
//
// This module owns no provider request, no model-to-encoding routing, no text
// splitting, and no cache. Splitting belongs to etl; routing belongs to
// whatever knows the model.
package tiktoken
