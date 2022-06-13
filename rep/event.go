package rep

import (
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p-core/peer"
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
	Code   RetrievalEventCode
	Status string
}

type RetrievalState struct {
	Root          cid.Cid
	Piece         cid.Cid
	PeerId        peer.ID
	ClientAddress peer.ID
	Finished      time.Time
}
