package interaction_test

import (
	"fmt"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
)

func ExampleNewSteerSignal() {
	id, err := agent.ParseSignalID("signal:user-correction")
	if err != nil {
		panic(err)
	}
	request, err := interaction.NewSteerSignal(
		id,
		chat.NewUserMessage(chat.NewTextPart("Use the newer requirements.")),
	)
	if err != nil {
		panic(err)
	}
	_, addressesWait := request.WaitID()

	fmt.Println(request.ID(), request.Valid(), addressesWait)
	// Output:
	// signal:user-correction true false
}

func ExampleToolCallRef() {
	// A Host persists the value returned by an invocation or result round's
	// Reference method, independently of its product record identity.
	retained, err := interaction.ParseToolCallRef("process:analysis/2/0")
	if err != nil {
		panic(err)
	}
	items := map[interaction.ToolCallRef]string{retained: "item-7"}
	encoded, err := retained.MarshalText()
	if err != nil {
		panic(err)
	}
	var restored interaction.ToolCallRef
	if err := restored.UnmarshalText(encoded); err != nil {
		panic(err)
	}
	fmt.Println(restored == retained, items[restored])
	fmt.Println(restored.ProcessID(), restored.ModelCallSequence(), restored.ToolCallIndex())
	// Output:
	// true item-7
	// process:analysis 2 0
}
