package rag

import "context"

// IdentityAugmenter keeps the original generation input rather than replacing
// it with the query's normalized retrieval text.
func IdentityAugmenter() AugmenterFunc {
	return AugmenterFunc(func(ctx context.Context, query Query, candidates Candidates) (Augmentation, error) {
		if err := ctx.Err(); err != nil {
			return Augmentation{}, err
		}
		if err := query.Validate(); err != nil {
			return Augmentation{}, err
		}
		if err := candidates.Validate(); err != nil {
			return Augmentation{}, err
		}
		return Augmentation{}, nil
	})
}
