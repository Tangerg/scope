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
