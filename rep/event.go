package rep

import (
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

type Code string

const (
	ConnectCode   Code = "connect"
	QueryAskCode  Code = "query-ask"
	ProposedCode  Code = "proposed"
	AcceptedCode  Code = "accepted"
	FirstByteCode Code = "first-byte-received"
	FailureCode   Code = "failure"
	SuccessCode   Code = "success"
)

type RetrievalEvent interface {
	Code() Code
	Phase() Phase
	PayloadCid() cid.Cid
	StorageProviderId() peer.ID
	StorageProviderAddr() address.Address
}

var (
	_ RetrievalEvent = RetrievalEventConnect{}
	_ RetrievalEvent = RetrievalEventQueryAsk{}
	_ RetrievalEvent = RetrievalEventProposed{}
	_ RetrievalEvent = RetrievalEventAccepted{}
	_ RetrievalEvent = RetrievalEventFirstByte{}
	_ RetrievalEvent = RetrievalEventFailure{}
	_ RetrievalEvent = RetrievalEventSuccess{}
)

type RetrievalEventConnect struct {
	phase               Phase
	payloadCid          cid.Cid
	storageProviderId   peer.ID
	storageProviderAddr address.Address
}

func NewRetrievalEventConnect(phase Phase, payloadCid cid.Cid, storageProviderId peer.ID, storageProviderAddr address.Address) RetrievalEventConnect {
	return RetrievalEventConnect{phase, payloadCid, storageProviderId, storageProviderAddr}
}

type RetrievalEventQueryAsk struct {
	phase               Phase
	payloadCid          cid.Cid
	storageProviderId   peer.ID
	storageProviderAddr address.Address
	queryResponse       retrievalmarket.QueryResponse
}

func NewRetrievalEventQueryAsk(phase Phase, payloadCid cid.Cid, storageProviderId peer.ID, storageProviderAddr address.Address, queryResponse retrievalmarket.QueryResponse) RetrievalEventQueryAsk {
	return RetrievalEventQueryAsk{phase, payloadCid, storageProviderId, storageProviderAddr, queryResponse}
}

type RetrievalEventProposed struct {
	phase               Phase
	payloadCid          cid.Cid
	storageProviderId   peer.ID
	storageProviderAddr address.Address
}

func NewRetrievalEventProposed(phase Phase, payloadCid cid.Cid, storageProviderId peer.ID, storageProviderAddr address.Address) RetrievalEventProposed {
	return RetrievalEventProposed{phase, payloadCid, storageProviderId, storageProviderAddr}
}

type RetrievalEventAccepted struct {
	phase               Phase
	payloadCid          cid.Cid
	storageProviderId   peer.ID
	storageProviderAddr address.Address
}

func NewRetrievalEventAccepted(phase Phase, payloadCid cid.Cid, storageProviderId peer.ID, storageProviderAddr address.Address) RetrievalEventAccepted {
	return RetrievalEventAccepted{phase, payloadCid, storageProviderId, storageProviderAddr}
}

type RetrievalEventFirstByte struct {
	phase               Phase
	payloadCid          cid.Cid
	storageProviderId   peer.ID
	storageProviderAddr address.Address
}

func NewRetrievalEventFirstByte(phase Phase, payloadCid cid.Cid, storageProviderId peer.ID, storageProviderAddr address.Address) RetrievalEventFirstByte {
	return RetrievalEventFirstByte{phase, payloadCid, storageProviderId, storageProviderAddr}
}

type RetrievalEventFailure struct {
	phase               Phase
	payloadCid          cid.Cid
	storageProviderId   peer.ID
	storageProviderAddr address.Address
	errorMessage        string
}

func NewRetrievalEventFailure(phase Phase, payloadCid cid.Cid, storageProviderId peer.ID, storageProviderAddr address.Address, errorMessage string) RetrievalEventFailure {
	return RetrievalEventFailure{phase, payloadCid, storageProviderId, storageProviderAddr, errorMessage}
}

type RetrievalEventSuccess struct {
	phase               Phase
	payloadCid          cid.Cid
	storageProviderId   peer.ID
	storageProviderAddr address.Address
	receivedSize        uint64
}

func NewRetrievalEventSuccess(phase Phase, payloadCid cid.Cid, storageProviderId peer.ID, storageProviderAddr address.Address, receivedSize uint64) RetrievalEventSuccess {
	return RetrievalEventSuccess{phase, payloadCid, storageProviderId, storageProviderAddr, receivedSize}
}

func (r RetrievalEventConnect) Code() Code                                    { return ConnectCode }
func (r RetrievalEventConnect) Phase() Phase                                  { return r.phase }
func (r RetrievalEventConnect) PayloadCid() cid.Cid                           { return r.payloadCid }
func (r RetrievalEventConnect) StorageProviderId() peer.ID                    { return r.storageProviderId }
func (r RetrievalEventConnect) StorageProviderAddr() address.Address          { return r.storageProviderAddr }
func (r RetrievalEventQueryAsk) Code() Code                                   { return QueryAskCode }
func (r RetrievalEventQueryAsk) Phase() Phase                                 { return r.phase }
func (r RetrievalEventQueryAsk) PayloadCid() cid.Cid                          { return r.payloadCid }
func (r RetrievalEventQueryAsk) StorageProviderId() peer.ID                   { return r.storageProviderId }
func (r RetrievalEventQueryAsk) StorageProviderAddr() address.Address         { return r.storageProviderAddr }
func (r RetrievalEventQueryAsk) QueryResponse() retrievalmarket.QueryResponse { return r.queryResponse }
func (r RetrievalEventProposed) Code() Code                                   { return ProposedCode }
func (r RetrievalEventProposed) Phase() Phase                                 { return r.phase }
func (r RetrievalEventProposed) PayloadCid() cid.Cid                          { return r.payloadCid }
func (r RetrievalEventProposed) StorageProviderId() peer.ID                   { return r.storageProviderId }
func (r RetrievalEventProposed) StorageProviderAddr() address.Address         { return r.storageProviderAddr }
func (r RetrievalEventAccepted) Code() Code                                   { return AcceptedCode }
func (r RetrievalEventAccepted) Phase() Phase                                 { return r.phase }
func (r RetrievalEventAccepted) PayloadCid() cid.Cid                          { return r.payloadCid }
func (r RetrievalEventAccepted) StorageProviderId() peer.ID                   { return r.storageProviderId }
func (r RetrievalEventAccepted) StorageProviderAddr() address.Address         { return r.storageProviderAddr }
func (r RetrievalEventFirstByte) Code() Code                                  { return FirstByteCode }
func (r RetrievalEventFirstByte) Phase() Phase                                { return r.phase }
func (r RetrievalEventFirstByte) PayloadCid() cid.Cid                         { return r.payloadCid }
func (r RetrievalEventFirstByte) StorageProviderId() peer.ID                  { return r.storageProviderId }
func (r RetrievalEventFirstByte) StorageProviderAddr() address.Address        { return r.storageProviderAddr }
func (r RetrievalEventFailure) Code() Code                                    { return FailureCode }
func (r RetrievalEventFailure) Phase() Phase                                  { return r.phase }
func (r RetrievalEventFailure) PayloadCid() cid.Cid                           { return r.payloadCid }
func (r RetrievalEventFailure) StorageProviderId() peer.ID                    { return r.storageProviderId }
func (r RetrievalEventFailure) StorageProviderAddr() address.Address          { return r.storageProviderAddr }
func (r RetrievalEventFailure) ErrorMessage() string                          { return r.errorMessage }
func (r RetrievalEventSuccess) Code() Code                                    { return SuccessCode }
func (r RetrievalEventSuccess) Phase() Phase                                  { return r.phase }
func (r RetrievalEventSuccess) PayloadCid() cid.Cid                           { return r.payloadCid }
func (r RetrievalEventSuccess) StorageProviderId() peer.ID                    { return r.storageProviderId }
func (r RetrievalEventSuccess) StorageProviderAddr() address.Address          { return r.storageProviderAddr }
func (r RetrievalEventSuccess) ReceivedSize() uint64                          { return r.receivedSize }
