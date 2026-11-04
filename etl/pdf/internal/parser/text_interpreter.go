package parser

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

type contentOperator string

const (
	operatorBeginText       contentOperator = "BT"
	operatorNextLine        contentOperator = "T*"
	operatorSetFont         contentOperator = "Tf"
	operatorSpacedNextText  contentOperator = "\""
	operatorNextLineText    contentOperator = "'"
	operatorShowText        contentOperator = "Tj"
	operatorShowTextArray   contentOperator = "TJ"
	operatorSaveGraphics    contentOperator = "q"
	operatorRestoreGraphics contentOperator = "Q"
	operatorPaintXObject    contentOperator = "Do"
)

// Each invocation owns its resources and saved graphics states. Forms inherit
// the selected font itself, because equal resource names can name different fonts.
type textState struct {
	resources Value
	font      *Font
	saved     []*Font
	fonts     map[string]*Font
}

func (t *textState) selectFont(name string) {
	if font := t.fonts[name]; font != nil {
		t.font = font
		return
	}
	value := t.resources.Key("Font").Key(name)
	if value.Kind() != Dict {
		panic(fmt.Errorf("missing font resource %q", name))
	}
	font := &Font{V: value}
	if t.fonts == nil {
		t.fonts = make(map[string]*Font)
	}
	t.fonts[name] = font
	t.font = font
}

func (t *textState) save(grant *budget) {
	grant.objects(1)
	grant.checkDepth(len(t.saved) + 1)
	t.saved = append(t.saved, t.font)
}

func (t *textState) restore() {
	if len(t.saved) == 0 {
		panic("graphics state restore without save")
	}
	index := len(t.saved) - 1
	t.font = t.saved[index]
	t.saved[index] = nil
	t.saved = t.saved[:index]
}

type textInterpreter struct {
	budget        *budget
	pageResources Value
	activeForms   map[objptr]bool
	text          strings.Builder
}

func (t *textInterpreter) interpret(content Value, state *textState) {
	t.budget.enter()
	defer t.budget.leave()
	Interpret(content, func(stack *Stack, operation string) {
		t.apply(state, stack, contentOperator(operation))
	})
	if len(state.saved) != 0 {
		panic("unbalanced graphics state saves")
	}
}

func (t *textInterpreter) apply(state *textState, stack *Stack, operation contentOperator) {
	args := popArgs(stack)
	switch operation {
	case operatorBeginText, operatorNextLine:
		requireOperands(operation, args)
		t.showText("\n")
	case operatorSetFont:
		requireOperands(operation, args, Name, Real)
		state.selectFont(args[0].Name())
	case operatorSpacedNextText:
		requireOperands(operation, args, Real, Real, String)
		t.showText("\n")
		t.showEncodedText(state, args[2].RawString())
	case operatorNextLineText, operatorShowText:
		requireOperands(operation, args, String)
		if operation == operatorNextLineText {
			t.showText("\n")
		}
		t.showEncodedText(state, args[0].RawString())
	case operatorShowTextArray:
		requireOperands(operation, args, Array)
		t.showTextArray(state, args[0])
	case operatorSaveGraphics:
		requireOperands(operation, args)
		state.save(t.budget)
	case operatorRestoreGraphics:
		requireOperands(operation, args)
		state.restore()
	case operatorPaintXObject:
		requireOperands(operation, args, Name)
		t.paintXObject(state, args[0].Name())
	}
}

func (t *textInterpreter) showText(text string) {
	t.budget.expand(int64(len(text)))
	t.text.WriteString(text)
}

func (t *textInterpreter) showEncodedText(state *textState, raw string) {
	if state.font == nil {
		panic("text has no selected font")
	}
	t.budget.expand(int64(len(raw)) * utf8.UTFMax)
	t.showText(state.font.Decoder().Decode(raw))
}

func (t *textInterpreter) showTextArray(state *textState, values Value) {
	for index := range values.Len() {
		value := values.Index(index)
		switch value.Kind() {
		case String:
			t.showEncodedText(state, value.RawString())
		case Integer, Real:
		default:
			panic("invalid TJ array entry")
		}
	}
}

func (t *textInterpreter) paintXObject(state *textState, name string) {
	object := state.resources.Key("XObject").Key(name)
	if object.Kind() != Stream {
		panic(fmt.Errorf("missing XObject stream %q", name))
	}
	switch object.Key("Subtype").Name() {
	case "Image":
		return
	case "Form":
	default:
		panic(fmt.Errorf("unsupported XObject subtype for %q", name))
	}
	if t.activeForms[object.ptr] {
		panic(fmt.Errorf("cyclic Form XObject %q (%d %d R)", name, object.ptr.id, object.ptr.gen))
	}
	resources := object.Key("Resources")
	if resources.IsNull() {
		// Legacy forms without Resources use the page, not a containing form.
		resources = t.pageResources
	} else if resources.Kind() != Dict {
		panic(fmt.Errorf("invalid Form Resources for %q", name))
	}
	t.activeForms[object.ptr] = true
	defer delete(t.activeForms, object.ptr)
	t.interpret(object, &textState{resources: resources, font: state.font})
}

func popArgs(stack *Stack) []Value {
	args := make([]Value, stack.Len())
	for index := len(args) - 1; index >= 0; index-- {
		args[index] = stack.Pop()
	}
	return args
}

func requireOperands(operation contentOperator, args []Value, kinds ...ValueKind) {
	if len(args) != len(kinds) {
		panic(fmt.Errorf("invalid %s operands", operation))
	}
	for index, kind := range kinds {
		actual := args[index].Kind()
		if actual != kind && (kind != Real || actual != Integer) {
			panic(fmt.Errorf("invalid %s operand %d", operation, index+1))
		}
	}
}
