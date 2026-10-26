package chatclient

import (
	"slices"
	"text/template"
	"text/template/parse"
)

const templateRootVariable = "$"

// Root identity follows lexical aliases, not field spelling. Template calls
// reset both dot and $, so the same tree can have two distinct input scopes.
type templateFields struct {
	compiled *template.Template
	names    map[string]struct{}
	visited  map[templateFieldCall]struct{}
}

func (t *templateFields) inspect(call templateFieldCall) {
	if _, visited := t.visited[call]; visited {
		return
	}
	compiled := t.compiled.Lookup(call.name)
	if compiled == nil || compiled.Tree == nil {
		return
	}
	t.visited[call] = struct{}{}
	scope := templateFieldScope{
		dot:       call.root,
		variables: []templateFieldVariable{{name: templateRootVariable, root: call.root}},
	}
	t.list(compiled.Root, &scope)
}

func (t *templateFields) list(list *parse.ListNode, scope *templateFieldScope) templateFieldFlow {
	frame := *scope
	flow := templateFieldFlow{normal: scope}
	if list == nil {
		return flow
	}
	for _, node := range list.Nodes {
		if flow.normal == nil {
			break
		}
		next := t.node(node, flow.normal)
		flow.normal = next.normal
		flow.breaks = frame.merge(flow.breaks, next.breaks)
		flow.continues = frame.merge(flow.continues, next.continues)
	}
	flow.normal = frame.merge(flow.normal)
	return flow
}

func (t *templateFields) node(node parse.Node, scope *templateFieldScope) templateFieldFlow {
	switch value := node.(type) {
	case *parse.ActionNode:
		t.pipeline(value.Pipe, scope)
	case *parse.IfNode:
		return t.branch(&value.BranchNode, scope)
	case *parse.WithNode:
		return t.branch(&value.BranchNode, scope)
	case *parse.RangeNode:
		return t.rangeFields(value, scope)
	case *parse.TemplateNode:
		t.inspect(templateFieldCall{name: value.Name, root: t.pipeline(value.Pipe, scope)})
	case *parse.BreakNode:
		return templateFieldFlow{breaks: scope}
	case *parse.ContinueNode:
		return templateFieldFlow{continues: scope}
	}
	return templateFieldFlow{normal: scope}
}

func (t *templateFields) expression(node parse.Node, scope *templateFieldScope) bool {
	switch value := node.(type) {
	case *parse.PipeNode:
		return t.pipeline(value, scope)
	case *parse.FieldNode:
		return t.field(scope.dot, value.Ident)
	case *parse.VariableNode:
		return t.field(scope.root(value.Ident[0]), value.Ident[1:])
	case *parse.ChainNode:
		return t.field(t.expression(value.Node, scope), value.Field)
	case *parse.DotNode:
		return scope.dot
	}
	return false
}

func (t *templateFields) field(root bool, path []string) bool {
	if len(path) == 0 {
		return root
	}
	if root {
		t.names[path[0]] = struct{}{}
	}
	return false
}

func (t *templateFields) pipeline(pipe *parse.PipeNode, scope *templateFieldScope) bool {
	if pipe == nil {
		return false
	}
	root := false
	for _, command := range pipe.Cmds {
		for _, argument := range command.Args {
			root = t.expression(argument, scope)
		}
		root = root && len(command.Args) == 1
	}
	root = root && len(pipe.Cmds) == 1
	for _, variable := range pipe.Decl {
		scope.bind(variable.Ident[0], root, pipe.IsAssign)
	}
	return root
}

func (t *templateFields) branch(branch *parse.BranchNode, scope *templateFieldScope) templateFieldFlow {
	entry := scope.clone()
	root := t.pipeline(branch.Pipe, &entry)
	accepted := entry.clone()
	if branch.NodeType == parse.NodeWith {
		accepted.dot = root
	}
	acceptedFlow := t.list(branch.List, &accepted)
	rejected := entry.clone()
	rejectedFlow := t.list(branch.ElseList, &rejected)
	return acceptedFlow.merge(rejectedFlow, *scope)
}

func (t *templateFields) rangeFields(loop *parse.RangeNode, scope *templateFieldScope) templateFieldFlow {
	entry := scope.clone()
	t.pipeline(loop.Pipe, &entry)
	empty := entry.clone()
	emptyFlow := t.list(loop.ElseList, &empty)
	iteration := entry.clone()
	iteration.dot = false
	var breaks *templateFieldScope
	for {
		body := iteration.clone()
		for _, variable := range loop.Pipe.Decl {
			body.bind(variable.Ident[0], false, true)
		}
		flow := t.list(loop.List, &body)
		breaks = entry.merge(breaks, flow.breaks)
		completion := entry.merge(flow.normal, flow.continues)
		// Only normal and continue paths can reach another iteration. Root
		// possibilities only grow, so the finite variable set bounds this pass.
		if completion == nil || !iteration.join(*completion) {
			// Go catches break around the whole range; else runs outside its
			// per-iteration continue boundary and can continue an outer loop.
			return templateFieldFlow{
				normal:    scope.merge(completion, breaks, emptyFlow.normal, emptyFlow.breaks),
				continues: scope.merge(emptyFlow.continues),
			}
		}
	}
}

// Distinct exits prevent a root alias assigned before break from entering the
// next iteration. A nil scope means that control cannot reach that exit.
type templateFieldFlow struct {
	normal    *templateFieldScope
	breaks    *templateFieldScope
	continues *templateFieldScope
}

func (t templateFieldFlow) merge(other templateFieldFlow, scope templateFieldScope) templateFieldFlow {
	return templateFieldFlow{
		normal:    scope.merge(t.normal, other.normal),
		breaks:    scope.merge(t.breaks, other.breaks),
		continues: scope.merge(t.continues, other.continues),
	}
}

type templateFieldCall struct {
	name string
	root bool
}

type templateFieldVariable struct {
	name string
	root bool
}

type templateFieldScope struct {
	dot       bool
	variables []templateFieldVariable
}

func (t templateFieldScope) clone() templateFieldScope {
	t.variables = slices.Clone(t.variables)
	return t
}

func (t templateFieldScope) root(name string) bool {
	for index := len(t.variables) - 1; index >= 0; index-- {
		if t.variables[index].name == name {
			return t.variables[index].root
		}
	}
	return false
}

func (t *templateFieldScope) bind(name string, root, assign bool) {
	if !assign {
		t.variables = append(t.variables, templateFieldVariable{name: name, root: root})
		return
	}
	for index := len(t.variables) - 1; index >= 0; index-- {
		if t.variables[index].name == name {
			t.variables[index].root = root
			return
		}
	}
}

// A continuation keeps only its enclosing variables and dot. Branch-local
// declarations must not escape through normal, break, or continue paths.
func (t templateFieldScope) merge(paths ...*templateFieldScope) *templateFieldScope {
	var merged *templateFieldScope
	for _, path := range paths {
		if path == nil {
			continue
		}
		if merged == nil {
			merged = &templateFieldScope{dot: t.dot, variables: slices.Clone(path.variables[:len(t.variables)])}
			continue
		}
		merged.join(*path)
	}
	return merged
}

func (t *templateFieldScope) join(other templateFieldScope) bool {
	changed := false
	for index := range t.variables {
		if !t.variables[index].root && other.variables[index].root {
			t.variables[index].root = true
			changed = true
		}
	}
	return changed
}
