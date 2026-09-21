// Package image defines the serializable image-generation protocol and its
// single-method [Model] capability.
//
// NewRequest captures the prompt; Options carries shared per-call overrides
// such as dimensions, negative prompt, seed, and output MIME type. Outputs use
// media.Media and preserve every image returned by the provider. Provider-only
// options use Options.Extensions.Set so Extensions remains JSON-safe; Request has no arbitrary
// parameter bag. Implementations and defaults live outside Core.
package image
