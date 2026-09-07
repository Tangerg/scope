// Package inmemory provides an in-process vector store backed by a map and a
// configurable similarity function. It is intended for demos, unit tests, and
// corpora that fit in RAM.
//
// Every public method is safe for concurrent use. Reads take a read lock and
// writes take an exclusive lock. Embedding calls may perform provider I/O.
// Numeric filters preserve the values of integers and finite floating-point
// numbers when comparing across representations; numeric strings remain strings.
//
// Records are not durable and disappear with the process.
package inmemory
