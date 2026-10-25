// Package skills exposes an Agent Skills
// [Source] as three model-callable
// tools for progressive disclosure:
//
//   - list_skills: every skill's name and description
//   - load_skill: one skill's instruction body, by exact name
//   - read_skill_resource: one bundled file under a skill
//
// Every result is a bounded JSON page with an optional next_cursor. Echo that
// cursor with the same name and path until it is absent. Text pages must be
// concatenated before treating instructions or resources as complete. Cursors
// bind the content and target; a changed source rejects a stale cursor and the
// caller starts again. No server-side pagination session is retained.
//
// Config.MaxResourceBytes bounds resource snapshots independently of page size.
// Files beyond that limit fail explicitly. List pages keep whole summaries and
// fail if one summary plus its cursor cannot fit Config.MaxOutputBytes.
//
// Bundled scripts are never executed here; the model runs them with its own
// shell or file tools.
package skills
