package vector

// Float32 narrows coordinates for stores that index in single precision.
// The storage boundary owns its metric's precision and magnitude constraints.
func Float32(source []float64) []float32 {
	if source == nil {
		return nil
	}
	output := make([]float32, len(source))
	for index, value := range source {
		output[index] = float32(value)
	}
	return output
}
