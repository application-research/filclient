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

func (us *unindexableStruct) RetrievalSubscriberId() interface{} {
	return "unindexableStruct_0"
}

func TestNoPanicOnUnindexableStruct(t *testing.T) {
	pub := rep.New()
	sub := &unindexableStruct{}
	unsub := pub.Subscribe(sub)
	pub.Subscribe(sub)

	if pub.SubscriberCount() != 1 {
		t.Fatal("should have deduped subscribers, but didn't")
	}

	unsub()
	if pub.SubscriberCount() != 0 {
		t.Fatal("should have unsubscribed but didn't")
	}
}
