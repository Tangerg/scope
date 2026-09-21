// Package moderation defines the serializable content-moderation protocol and
// its single-method [Model] capability.
//
// A Request can classify multiple texts. Each Output retains provider category
// names and their Verdict values, while Categories.Flagged provides the
// aggregate decision.
// Provider-only options use Options.Extensions.Set so Extensions remains JSON-safe; Request has
// no arbitrary parameter bag. Implementations and defaults live outside Core.
package moderation
