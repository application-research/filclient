package filclient

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"golang.org/x/xerrors"

	"github.com/filecoin-project/boost/transport/httptransport"
	boosttypes "github.com/filecoin-project/boost/transport/types"
	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p-core/peer"
)

// libp2pTransferManager watches events for a libp2p data transfer
type libp2pTransferManager struct {
	dtServer    *httptransport.Libp2pCarServer
	authDB      *httptransport.AuthTokenDB
	xferTimeout time.Duration
	cancelCtx   context.CancelFunc
	ticker      *time.Ticker

	listenerLk sync.Mutex
	listener   func(uint, ChannelState)
	unsub      func()
}

func newLibp2pTransferManager(dtServer *httptransport.Libp2pCarServer, authDB *httptransport.AuthTokenDB, xferTimeout time.Duration) *libp2pTransferManager {
	return &libp2pTransferManager{
		dtServer:    dtServer,
		authDB:      authDB,
		xferTimeout: xferTimeout,
	}
}

func (m *libp2pTransferManager) Stop() {
	m.ticker.Stop()
	m.cancelCtx()
	m.unsub()
}

func (m *libp2pTransferManager) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	m.cancelCtx = cancel

	// Every minute, check transfers to see if any have expired
	m.ticker = time.NewTicker(time.Minute)
	go func() {
		for range m.ticker.C {
			m.checkTransferExpiry(ctx)
		}
	}()

	return m.dtServer.Start(ctx)
}

func (m *libp2pTransferManager) checkTransferExpiry(ctx context.Context) {
	// Delete expired auth tokens from the auth DB
	expired, err := m.authDB.DeleteExpired(ctx, time.Now().Add(-m.xferTimeout))
	if err != nil {
		log.Errorf("deleting expired tokens from auth DB: %w", err)
		return
	}

	// For each expired auth token
	for _, val := range expired {
		// Get the transfer associated with the auth token
		xfer, err := m.dtServer.Get(val.ID)
		if err != nil && !xerrors.Is(err, httptransport.ErrTransferNotFound) {
			log.Errorf("canceling transfer %s: %w", val.ID, err)
			continue
		}

		transferFound := err == nil || !xerrors.Is(err, httptransport.ErrTransferNotFound)
		if transferFound {
			// Cancel the transfer
			st, err := m.dtServer.CancelTransfer(ctx, val.ID)
			if err != nil && !xerrors.Is(err, httptransport.ErrTransferNotFound) {
				log.Errorf("canceling transfer %s: %w", val.ID, err)
				continue
			}

			// If the transfer had already completed, nothing more to do
			if st.Status == boosttypes.TransferStatusCompleted {
				continue
			}
		}

		// The transfer didn't start, or was canceled or errored out before
		// completing, so fire a transfer error event
		m.listenerLk.Lock()
		if m.listener != nil {
			dbid, err := strconv.ParseUint(val.ID, 10, 64)
			if err != nil {
				log.Warnf("cannot parse dbid '%s' in libp2p transfer manager event: %s", val.ID, err)
				return
			}

			st := boosttypes.TransferState{
				LocalAddr: m.dtServer.ID().String(),
			}
			if transferFound {
				st = xfer.State()
			}
			st.Status = boosttypes.TransferStatusFailed
			if st.Message == "" {
				st.Message = fmt.Sprintf("timed out waiting %s for transfer to complete", m.xferTimeout)
			}

			m.listener(uint(dbid), m.toDTState(st))
		}
		m.listenerLk.Unlock()
	}
}

// PrepareForDataRequest prepares to receive a data transfer request with the
// given auth token
func (m *libp2pTransferManager) PrepareForDataRequest(ctx context.Context, id uint, authToken string, proposalCid cid.Cid, payloadCid cid.Cid, size uint64) error {
	err := m.authDB.Put(ctx, authToken, httptransport.AuthValue{
		ID:          fmt.Sprintf("%s", id),
		ProposalCid: proposalCid,
		PayloadCid:  payloadCid,
		Size:        size,
	})
	if err != nil {
		return fmt.Errorf("adding new auth token: %w", err)
	}
	return nil
}

// CleanupPreparedRequest is called to remove the auth token for a request
// when the request is no longer expected (eg because the provider rejected
// the deal proposal)
func (m *libp2pTransferManager) CleanupPreparedRequest(ctx context.Context, dbid uint, authToken string) error {
	// Delete the auth token for the request
	delerr := m.authDB.Delete(ctx, authToken)

	// Cancel any related transfer
	dbidstr := fmt.Sprintf("%d", dbid)
	_, cancelerr := m.dtServer.CancelTransfer(ctx, dbidstr)
	if cancelerr != nil && xerrors.Is(cancelerr, httptransport.ErrTransferNotFound) {
		// Ignore transfer not found error
		cancelerr = nil
	}

	if delerr != nil {
		return delerr
	}
	return cancelerr
}

// Subscribe to state change events from the libp2p server
func (m *libp2pTransferManager) Subscribe(listener func(dbid uint, st ChannelState)) (func(), error) {
	m.listenerLk.Lock()
	defer m.listenerLk.Unlock()

	if m.listener != nil {
		// Only need one for the current use case, we can add more if needed later
		return nil, fmt.Errorf("only one listener allowed")
	}

	m.listener = listener
	m.unsub = m.subscribe(m.listener)
	return m.unsub, nil
}

// Convert from libp2p server events to data transfer events
func (m *libp2pTransferManager) subscribe(cb func(uint, ChannelState)) (unsub func()) {
	return m.dtServer.Subscribe(func(dbidstr string, st boosttypes.TransferState) {
		dbid, err := strconv.ParseUint(dbidstr, 10, 64)
		if err != nil {
			log.Warnf("cannot parse dbid '%s' in libp2p transfer manager event: %s", dbidstr, err)
			return
		}
		if st.Status == boosttypes.TransferStatusFailed {
			// If the transfer fails, don't fire the failure event yet.
			// Wait until the transfer timeout (so that the data receiver has
			// a chance to make another request)
			log.Infow("libp2p data transfer error", "dbid", dbid, "remote", st.RemoteAddr, "message", st.Message)
			return
		}
		cb(uint(dbid), m.toDTState(st))
	})
}

func (m *libp2pTransferManager) toDTState(st boosttypes.TransferState) ChannelState {
	status := datatransfer.Ongoing
	switch st.Status {
	case boosttypes.TransferStatusStarted:
		status = datatransfer.Requested
	case boosttypes.TransferStatusCompleted:
		status = datatransfer.Completed
	case boosttypes.TransferStatusFailed:
		status = datatransfer.Failed
	}
	return ChannelState{
		SelfPeer:   peer.ID(st.LocalAddr),
		RemotePeer: peer.ID(st.RemoteAddr),
		Status:     status,
		StatusStr:  datatransfer.Statuses[status],
		Sent:       st.Sent,
		Received:   st.Received,
		Message:    st.Message,
	}
}
