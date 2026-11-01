package messaging_test

import (
	"fmt"

	"github.com/Tangerg/scope/agent/messaging"
	"github.com/Tangerg/scope/agent/run"
)

func ExampleMessage_Effect() {
	recipient, err := run.ParseProcessID("process:review-coordinator")
	if err != nil {
		panic(err)
	}
	payload, err := run.EncodePayload("The proposed budget needs revision.")
	if err != nil {
		panic(err)
	}
	message := messaging.Message{Recipient: recipient, Payload: payload}
	effect, err := message.Effect()
	if err != nil {
		panic(err)
	}
	// Return this Effect from Step and bind messaging.Dispatcher to the
	// Program. The Host's DeliveryPort authorizes this concrete recipient.
	fmt.Println(effect.Target())
	fmt.Println(message.Recipient)
	// Output:
	// dispatcher
	// process:review-coordinator
}
