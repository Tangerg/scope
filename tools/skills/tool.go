package skills

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
	skillsrc "github.com/Tangerg/scope/skills"
)

type ListSkillsRequest struct {
	Cursor *Cursor `json:"cursor,omitzero" jsonschema_description:"Echo next_cursor from the preceding page; omit to start a new listing."`
}

// LoadSkillRequest uses an exact name; it does not search or broaden the selection.
type LoadSkillRequest struct {
	Cursor *Cursor `json:"cursor,omitzero" jsonschema_description:"Echo next_cursor to continue this exact skill; omit to start."`
	Name   string  `json:"name" jsonschema:"minLength=1" jsonschema_description:"Exact skill name returned by list_skills or already known from the prompt."`
}

// Resource reads remain confined to the named skill.
type ReadSkillResourceRequest struct {
	Cursor *Cursor `json:"cursor,omitzero" jsonschema_description:"Echo next_cursor to continue this exact resource; omit to start."`
	Name   string  `json:"name" jsonschema:"minLength=1" jsonschema_description:"Exact name of the skill whose instructions referenced the resource."`
	Path   string  `json:"path" jsonschema:"minLength=1" jsonschema_description:"Resource path relative to the skill directory, such as references/REFERENCE.md or scripts/run.py."`
}

type toolSet struct {
	source           Source
	maxOutputBytes   int64
	maxResourceBytes int64
}

const (
	DefaultMaxOutputBytes = int64(256 * 1024)
	minimumMaxOutputBytes = int64(160)
	maximumMaxOutputBytes = int64(^uint(0)>>1) - 1
)

// Config bounds each serialized JSON page independently of resource reads.
// MaxResourceBytes bounds a complete resource snapshot; larger files fail instead
// of silently losing their suffix. Zero fields select the package defaults.
type Config struct {
	MaxOutputBytes   int64
	MaxResourceBytes int64
}

// Source provides the operations exposed by the three skill Tools. List and Load
// follow skills.Source's validity, cancellation, and detached-value contracts.
type Source interface {
	skillsrc.ResourceOpener
	List(ctx context.Context) ([]skillsrc.Summary, error)
	Load(ctx context.Context, name string) (*skillsrc.Skill, error)
}

func NewTools(source Source, config Config) ([]toolcontract.Tool, error) {
	if lo.IsNil(source) {
		return nil, ErrNilSource
	}
	if config.MaxOutputBytes < 0 {
		return nil, fmt.Errorf("%w: maximum output bytes must not be negative", skillsrc.ErrInvalidLimit)
	}
	maxOutputBytes := config.MaxOutputBytes
	if maxOutputBytes == 0 {
		maxOutputBytes = DefaultMaxOutputBytes
	}
	if maxOutputBytes < minimumMaxOutputBytes || maxOutputBytes > maximumMaxOutputBytes {
		return nil, fmt.Errorf(
			"%w: maximum output bytes must be between %d and %d",
			skillsrc.ErrInvalidLimit,
			minimumMaxOutputBytes,
			maximumMaxOutputBytes,
		)
	}
	maxResourceBytes := config.MaxResourceBytes
	if maxResourceBytes == 0 {
		maxResourceBytes = skillsrc.DefaultMaxResourceBytes
	}
	if maxResourceBytes < 1 || maxResourceBytes > maximumMaxOutputBytes {
		return nil, fmt.Errorf("%w: maximum resource bytes must be positive and safely bounded", skillsrc.ErrInvalidLimit)
	}
	set := &toolSet{source: source, maxOutputBytes: maxOutputBytes, maxResourceBytes: maxResourceBytes}
	list, err := toolcontract.NewFunc(
		toolcontract.FuncConfig{
			Name: "list_skills",
			Description: "List a page of visible skill names and descriptions. Echo next_cursor until absent to finish discovery. " +
				"Use this when a relevant skill may exist but its exact name is unknown. Load a matching skill with load_skill before following it.",
		},
		set.list,
	)
	if err != nil {
		return nil, fmt.Errorf("skills: build list_skills: %w", err)
	}
	load, err := toolcontract.NewFunc(
		toolcontract.FuncConfig{
			Name: "load_skill",
			Description: "Read instructions for one exact skill name. Echo next_cursor until absent and concatenate content before following the instructions. Call this when the task matches that skill's description, " +
				"then follow the returned instructions. Use read_skill_resource only for a bundled file referenced by those instructions.",
		},
		set.load,
	)
	if err != nil {
		return nil, fmt.Errorf("skills: build load_skill: %w", err)
	}
	readResource, err := toolcontract.NewFunc(
		toolcontract.FuncConfig{
			Name: "read_skill_resource",
			Description: "Read a page of one bundled resource referenced by a loaded skill. Echo next_cursor until absent and concatenate content to read the complete file. The path is relative to that skill's directory. " +
				"This returns the resource as UTF-8 text only and never executes scripts; a resource that is not valid UTF-8 is rejected rather than returned as binary.",
		},
		set.readResource,
	)
	if err != nil {
		return nil, fmt.Errorf("skills: build read_skill_resource: %w", err)
	}
	return []toolcontract.Tool{list, load, readResource}, nil
}

func (t *toolSet) list(ctx context.Context, request ListSkillsRequest) (ListPage, error) {
	if err := request.Cursor.validate(); err != nil {
		return ListPage{}, inputFailure(err)
	}
	summaries, err := t.source.List(ctx)
	if err != nil {
		return ListPage{}, err
	}
	encoded, err := jsonv2.Marshal(summaries)
	if err != nil {
		return ListPage{}, err
	}
	snapshot := contentSnapshot("list", "", "", encoded)
	start, err := request.Cursor.start(snapshot, len(summaries))
	if err != nil {
		return ListPage{}, inputFailure(err)
	}
	page, err := boundedPage(len(summaries)-start, t.maxOutputBytes, func(count int) ListPage {
		end := start + count
		page := ListPage{Skills: summaries[start:end]}
		if end < len(summaries) {
			page.NextCursor = &Cursor{Offset: end, Snapshot: snapshot}
		}
		return page
	})
	if err != nil {
		return ListPage{}, inputFailure(err)
	}
	return page, nil
}

func (t *toolSet) load(ctx context.Context, request LoadSkillRequest) (TextPage, error) {
	if err := skillsrc.ValidateName(request.Name); err != nil {
		return TextPage{}, inputFailure(err)
	}
	if err := request.Cursor.validate(); err != nil {
		return TextPage{}, inputFailure(err)
	}
	skill, err := t.source.Load(ctx, request.Name)
	if err != nil {
		return TextPage{}, err
	}
	page, err := textPage("load", request.Name, "", skill.Instructions, request.Cursor, t.maxOutputBytes)
	if err != nil {
		return TextPage{}, inputFailure(err)
	}
	return page, nil
}

func (t *toolSet) readResource(ctx context.Context, request ReadSkillResourceRequest) (TextPage, error) {
	if err := skillsrc.ValidateResourceRequest(request.Name, request.Path); err != nil {
		return TextPage{}, inputFailure(err)
	}
	if err := request.Cursor.validate(); err != nil {
		return TextPage{}, inputFailure(err)
	}
	data, truncated, err := skillsrc.ReadResource(ctx, t.source, request.Name, request.Path, t.maxResourceBytes)
	if err != nil {
		return TextPage{}, err
	}
	if truncated {
		return TextPage{}, inputFailure(fmt.Errorf("%w: resource exceeds %d bytes", skillsrc.ErrInvalidLimit, t.maxResourceBytes))
	}
	page, err := textPage("resource", request.Name, request.Path, string(data), request.Cursor, t.maxOutputBytes)
	if err != nil {
		return TextPage{}, inputFailure(err)
	}
	return page, nil
}

// inputFailure reports rejected requests and unreadable pages as definite,
// model-visible failures. These read-only tools have no uncertain external effect.
func inputFailure(cause error) error {
	failure, err := toolcontract.NewFailure(toolcontract.FailureConfig{
		Kind:   toolcontract.FailureKindFailed,
		Output: chat.NewTextToolOutput(cause.Error()),
		Cause:  cause,
	})
	if err != nil {
		return errors.Join(cause, err)
	}
	return failure
}
