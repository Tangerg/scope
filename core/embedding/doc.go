// Package embedding defines the stable text-to-vector protocol and its
// single-method provider SPI.
//
// Build input with NewRequest and attach Options only for per-call overrides.
// Provider defaults and identity are fixed when constructing an implementation,
// not exposed through Model. Dimension discovery belongs to the consuming
// workflow because it requires an actual embedding request. Numeric coordinates
// and storage precision projections belong to core/vector.
// Provider options use Options.Extensions.Set so Extensions remains
// JSON-safe; Request has no arbitrary parameter bag.
package embedding
