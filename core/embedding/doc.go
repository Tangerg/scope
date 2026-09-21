// Package embedding defines the stable text-to-vector protocol and its
// single-method provider SPI.
//
// Build input with NewRequest and attach Options only for per-call overrides.
// Provider defaults and identity are fixed when constructing an implementation,
// not exposed through Model. Dimension discovery belongs to the consuming
// workflow because it requires an actual embedding request. [Float32Vector]
// bridges the protocol's float64 representation to storage SDKs that require
// float32. Provider options use Options.Extensions.Set so Extensions remains
// JSON-safe; Request has no arbitrary parameter bag.
package embedding
