// Package skills is a read-only repository over Agent Skills
// (https://agentskills.io) — directories that each hold a SKILL.md
// (YAML frontmatter + Markdown instructions) plus optional bundled resources
// under references/, assets/, and scripts/.
//
// It exposes [Source] for List/Lookup/Load and [ResourceSource] for bundled
// files read on demand. [NewRepository] wraps any fs.FS;
// [NewDirectoryRepository] confines a real directory. [Overlay] layers sources
// by precedence.
//
// The package parses, validates, and serves skill content. It does not execute
// scripts, which an agent runs with its own shell and file tools, and it does
// not know about chat models or tools; the model-callable adapter lives in
// tools/skills.
package skills
