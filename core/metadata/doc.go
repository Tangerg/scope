// Package metadata provides JSON-safe extension values for Core protocol
// types.
//
// Map stores each entry as validated JSON rather than arbitrary Go objects.
// Use Set or FromValues at write boundaries and Decode or Values at read
// boundaries. This prevents callbacks, readers, SDK clients, and other runtime
// state from entering serializable requests, responses, documents, or media.
// Values preserves JSON numbers as json.Number; Decode uses the caller's target
// type and its numeric representation limits.
package metadata
