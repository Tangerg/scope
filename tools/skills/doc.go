// Package skills exposes an Agent Skills
// [github.com/Tangerg/scope/skills.ResourceSource] as three model-callable
// tools for progressive disclosure:
//
//   - list_skills: every skill's name and description
//   - load_skill: one skill's instruction body, by exact name
//   - read_skill_resource: one bundled file under a skill
//
// Bundled scripts are never executed here; the model runs them with its own
// shell or file tools.
package skills
