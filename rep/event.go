package rep

import (
	"time"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-fil-markets/retrievalmarket"
	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p-core/peer"
)

type Phase string

const (
	// QueryPhase involves a connect, query-ask, success|failure
	QueryPhase Phase = "query"
	// RetrievalPhase involves the full data retrieval: connect, proposed, accepted, first-byte-received, success|failure
	RetrievalPhase Phase = "retrieval"
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
	Phase Phase
	Code  RetrievalEventCode
	// Status will include error messages for RetrievalEventFailure, other events use it to expand the Code description
	Status string
	// QueryResponse will be included for a RetrievalEventQueryAsk event
	QueryResponse *retrievalmarket.QueryResponse
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
	ReceivedSize        uint64
}
