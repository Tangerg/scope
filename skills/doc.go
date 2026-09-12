// Package skills is a read-only repository over Agent Skills
// (https://agentskills.io) — directories that each hold a SKILL.md
// (YAML frontmatter + Markdown instructions) plus optional bundled resources
// under references/, assets/, and scripts/.
//
// It exposes [Source] for List/Lookup/Load and [ResourceSource] for bundled files
// read on demand. [NewRepository] wraps any fs.FS;
// [NewDirectoryRepository] confines a real directory.
// ResourceSource owns skill validation and regular-file checks. ErrSkillNotFound
// distinguishes an absent skill from a missing resource, so Overlay selects a
// resource source without loading the winning skill a second time.
// List and Lookup read bounded metadata; a complete document may still exceed
// Load's limit or fail its validation. Overlay preserves these disclosure levels.
//
// The package is deliberately minimal: it parses, validates, and serves skill
// content. It does NOT execute scripts — an agent runs those with its own
// shell/file tools — and it does NOT know about chat models or tools. The
// LLM-callable wrapper lives in tools/skills, a thin adapter over
// ResourceSource.
package skills
