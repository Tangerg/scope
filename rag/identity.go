package rag

import "context"

// IdentityAugmenter preserves the query text when retrieval evidence is needed
// without prompt augmentation.
func IdentityAugmenter() Augmenter {
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
		return NewAugmentation(query.Text())
	})
}
