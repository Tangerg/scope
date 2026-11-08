// Package skills is a read-only repository over Agent Skills
// (https://agentskills.io) — directories that each hold a SKILL.md
// (YAML frontmatter + Markdown instructions) plus optional bundled resources
// under references/, assets/, and scripts/.
//
// It exposes [Source] for List/Lookup/Load and [ResourceSource] for bundled
// files read on demand. [NewRepository] requires a [ResourceFS] whose resource
// opens remain within the selected skill directory; an ordinary fs.FS cannot
// grant this capability. [NewDirectoryRepository] borrows an os.Root and uses a
// per-skill subroot to enforce it. It rejects js and plan9 because their os.Root
// implementations cannot preserve this authority during filesystem changes.
// Embedded and archive backends implement the same contract without treating
// fs.Sub as a security boundary. [Overlay] preserves the winning source's
// complete bundle and its confinement guarantee.
//
// The package parses, validates, and serves skill content. It does not execute
// scripts, which an agent runs with its own shell and file tools, and it does
// not know about chat models or tools; the model-callable adapter lives in
// tools/skills.
package skills
