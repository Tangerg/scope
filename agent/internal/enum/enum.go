// Package enum owns how agent vocabularies render. A value outside its
// vocabulary prints as one fixed word, so an unvalidated string never reaches
// a log or diagnostic verbatim.
package enum

const invalidName = "invalid"

type vocabulary interface {
	~string
	Valid() bool
}

func String[T vocabulary](value T) string {
	if !value.Valid() {
		return invalidName
	}
	return string(value)
}
