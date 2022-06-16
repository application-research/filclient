package rep_test

import (
	"testing"

	"github.com/application-research/filclient/rep"
)

type unindexableStruct struct {
	events []rep.RetrievalEvent
}

func (us *unindexableStruct) OnRetrievalEvent(evt rep.RetrievalEvent) {
	us.events = append(us.events, evt)
}

func (us *unindexableStruct) SubscriberIdentifier() interface{} {
	return "unindexable"
}
func TestNoPanicOnUnindexableStruct(t *testing.T) {
	pub := rep.New()
	sub := &unindexableStruct{}
	pub.Subscribe(sub)
	pub.Subscribe(sub)

	if pub.NumSubscribers() != 1 {
		t.Fatal("should have deduped subscribers, but didn't")
	}

	pub.Unsubscribe(sub)
	if pub.NumSubscribers() != 0 {
		t.Fatal("should have unsubscribed but didn't")
	}
}
