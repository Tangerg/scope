// Package vector owns finite numeric coordinates and their JSON representation.
// Embedding models and vector stores share these values; dimensions, metrics,
// encoder-space identity, and operation errors belong to their consuming domain.
// Float32 narrows values at the native storage boundary, whose precision and
// magnitude constraints determine whether the projection can be indexed.
package vector
