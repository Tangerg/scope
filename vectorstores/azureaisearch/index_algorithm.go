package azureaisearch

import "fmt"

type indexAlgorithmKind string

const (
	indexAlgorithmHNSW          indexAlgorithmKind = "hnsw"
	indexAlgorithmExhaustiveKNN indexAlgorithmKind = "exhaustiveKnn"
)

type indexAlgorithmParameters struct {
	Metric nativeMetric `json:"metric"`
}

type indexAlgorithm struct {
	Name                    string                    `json:"name"`
	Kind                    indexAlgorithmKind        `json:"kind"`
	HNSWParameters          *indexAlgorithmParameters `json:"hnswParameters"`
	ExhaustiveKNNParameters *indexAlgorithmParameters `json:"exhaustiveKnnParameters"`
}

func (i indexAlgorithm) metric() (nativeMetric, error) {
	if i.HNSWParameters != nil && i.ExhaustiveKNNParameters != nil {
		return "", fmt.Errorf("%w: algorithm %q declares multiple parameter blocks", ErrIncompatibleIndex, i.Name)
	}
	var parameters *indexAlgorithmParameters
	switch i.Kind {
	case indexAlgorithmHNSW:
		parameters = i.HNSWParameters
	case indexAlgorithmExhaustiveKNN:
		parameters = i.ExhaustiveKNNParameters
	default:
		return "", fmt.Errorf("%w: algorithm %q has unsupported kind %q", ErrIncompatibleIndex, i.Name, i.Kind)
	}
	if parameters == nil {
		return "", fmt.Errorf("%w: algorithm %q declares no parameters for kind %q", ErrIncompatibleIndex, i.Name, i.Kind)
	}
	if !parameters.Metric.valid() {
		return "", fmt.Errorf("%w: algorithm %q declares unsupported metric %q", ErrIncompatibleIndex, i.Name, parameters.Metric)
	}
	return parameters.Metric, nil
}
