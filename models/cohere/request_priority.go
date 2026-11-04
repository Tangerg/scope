package cohere

import "fmt"

type requestPriority int

const (
	minimumRequestPriority requestPriority = 0
	maximumRequestPriority requestPriority = 999
)

func (r requestPriority) validate() error {
	if r < minimumRequestPriority || r > maximumRequestPriority {
		return fmt.Errorf("cohere: priority must be between %d and %d", minimumRequestPriority, maximumRequestPriority)
	}
	return nil
}
