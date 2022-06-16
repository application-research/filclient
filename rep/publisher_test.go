package rep_test

import (
	"testing"

	"github.com/application-research/filclient/rep"
)

type unindexableStruct struct {
	ints []int
}

func (us unindexableStruct) OnRetrievalEvent(rep.RetrievalEvent) {

}

func TestNoPanicOnUnindexableStruct(t *testing.T) {
	pub := rep.New()
	pub.Subscribe(unindexableStruct{})
	pub.Subscribe(unindexableStruct{})
}
