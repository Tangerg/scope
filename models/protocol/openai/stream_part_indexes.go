package openai

// streamPartIndexes numbers one stream's native parts in first-seen order. A
// key must capture every native distinction between parts, because equal keys
// extend one Core part.
type streamPartIndexes[K comparable] struct {
	indexes map[K]int
}

func (s *streamPartIndexes[K]) of(key K) int {
	if index, found := s.indexes[key]; found {
		return index
	}
	if s.indexes == nil {
		s.indexes = make(map[K]int)
	}
	index := len(s.indexes)
	s.indexes[key] = index
	return index
}
