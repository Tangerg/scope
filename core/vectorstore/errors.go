package vectorstore

import "errors"

var (
	ErrInvalidOptions = errors.New("vectorstore: invalid options")

	ErrInvalidRequest = errors.New("vectorstore: invalid request")

	ErrInvalidResponse = errors.New("vectorstore: invalid response")

	ErrInvalidScore = errors.New("vectorstore: invalid score")

	ErrEmptyDocuments = errors.New("vectorstore: documents must not be empty")

	ErrInvalidDocument = errors.New("vectorstore: invalid document")

	ErrMissingDocumentID = errors.New("vectorstore: document ID is required")

	ErrDuplicateDocumentID = errors.New("vectorstore: duplicate document ID")

	ErrMissingFilter = errors.New("vectorstore: filter is required")

	ErrUnsupportedSearchMode = errors.New("vectorstore: unsupported search mode")
)
