package rep

import (
	"time"

	"github.com/filecoin-project/go-address"
	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p-core/peer"
)

type EventType string

const (
	RetrievalQueryEvent  EventType = "query"
	RetrieveContentEvent EventType = "retrieve"
)

type RetrievalEventCode string

const (
	RetrievalEventConnect   RetrievalEventCode = "connect"
	RetrievalEventQueryAsk  RetrievalEventCode = "query-ask"
	RetrievalEventProposed  RetrievalEventCode = "proposed"
	RetrievalEventAccepted  RetrievalEventCode = "accepted"
	RetrievalEventFirstByte RetrievalEventCode = "first-byte-received"
	RetrievalEventFailure   RetrievalEventCode = "failure"
	RetrievalEventSuccess   RetrievalEventCode = "success"
)

type RetrievalEvent struct {
	Type   EventType
	Code   RetrievalEventCode
	Status string
}

// TODO: This is moreso retrieval properties than state. If this
// needs to be stateful in the future, implement as a state machine.
type RetrievalState struct {
	PayloadCid          cid.Cid
	PieceCid            *cid.Cid
	StorageProviderID   peer.ID
	StorageProviderAddr address.Address
	ClientID            peer.ID
	FinishedTime        time.Time
}
