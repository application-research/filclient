package filclient

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/filecoin-project/boost/transport/httptransport"
	boosttypes "github.com/filecoin-project/boost/transport/types"
	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/sync"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"
)

func TestLibp2pTransferManager(t *testing.T) {
	runTest := func(t *testing.T, completeBeforeExpiry bool) {
		opts := libp2pTransferManagerOpts{
			xferTimeout:     100 * time.Millisecond,
			authCheckPeriod: 200 * time.Millisecond,
		}
		ctx, cancel := context.WithTimeout(context.Background(), opts.authCheckPeriod*2)
		defer cancel()

		ds := sync.MutexWrap(datastore.NewMapDatastore())
		server := &mockLibp2pCarServer{}
		authDB := httptransport.NewAuthTokenDB(ds)

		tm := newLibp2pTransferManager(server, ds, authDB, opts)

		type evt struct {
			dbid uint
			st   ChannelState
		}
		evts := make(chan evt, 1024)
		unsub, err := tm.Subscribe(func(dbid uint, st ChannelState) {
			evts <- evt{dbid: dbid, st: st}
		})
		require.NoError(t, err)
		defer unsub()

		err = tm.Start(ctx)
		require.NoError(t, err)

		authToken, err := httptransport.GenerateAuthToken()
		require.NoError(t, err)
		proposalCid, err := cid.Parse("bafkqaaa")
		require.NoError(t, err)
		payloadCid, err := cid.Parse("bafkqaab")
		require.NoError(t, err)
		dbid := uint(1)
		size := uint64(1024)
		err = tm.PrepareForDataRequest(ctx, dbid, authToken, proposalCid, payloadCid, size)
		require.NoError(t, err)

		// Expect an "Ongoing" event to be passed through to the subscriber
		dbidstr := fmt.Sprintf("%d", dbid)
		st := boosttypes.TransferState{
			Status: boosttypes.TransferStatusOngoing,
		}
		server.simulateEvent(dbidstr, st)
		require.Len(t, evts, 1)
		e := <-evts
		require.Equal(t, datatransfer.Ongoing, e.st.Status)

		if completeBeforeExpiry {
			// Simulate a "Completed" event
			st := boosttypes.TransferState{
				Status: boosttypes.TransferStatusCompleted,
			}
			server.simulateEvent(dbidstr, st)
			require.Len(t, evts, 1)
			e := <-evts
			require.Equal(t, datatransfer.Completed, e.st.Status)
		}

		// Simulate a Failed event
		st = boosttypes.TransferState{
			Status: boosttypes.TransferStatusFailed,
		}
		server.simulateEvent(dbidstr, st)
		require.Len(t, evts, 0)

		// Wait until the auth token expires, and the auth token check runs
		select {
		case <-ctx.Done():
			// The server fired a Complete event so we expected the manager not
			// to fire a Failed event
			if completeBeforeExpiry {
				return
			}

			// The server did not fire a Complete event, so we were expecting
			// the manager to fire a Failed event
			require.Fail(t, "timed out waiting for Failed event")
		case e = <-evts:
			// The server fired a Complete event, so we did not expect the
			// manager to fire a Failed event
			if completeBeforeExpiry {
				require.Fail(t, "unexpected event %s", e.st.StatusStr)
				return
			}

			// The server did not fire a Complete event, so we expect the
			// manager to fire a Failed event
			require.Equal(t, datatransfer.Failed, e.st.Status)
		}
	}

	t.Run("pass if complete event before token expiry", func(t *testing.T) {
		runTest(t, true)
	})

	t.Run("fail if no complete event", func(t *testing.T) {
		runTest(t, false)
	})
}

type mockLibp2pCarServer struct {
	listener httptransport.EventListenerFn
}

func (m *mockLibp2pCarServer) simulateEvent(dbidstr string, st boosttypes.TransferState) {
	m.listener(dbidstr, st)
}

func (m *mockLibp2pCarServer) Subscribe(listener httptransport.EventListenerFn) httptransport.UnsubFn {
	m.listener = listener
	return func() {}
}

func (m *mockLibp2pCarServer) ID() peer.ID {
	return "pid"
}

func (m *mockLibp2pCarServer) Start(ctx context.Context) error {
	return nil
}

func (m *mockLibp2pCarServer) Get(id string) (*httptransport.Libp2pTransfer, error) {
	return nil, nil
}

func (m *mockLibp2pCarServer) CancelTransfer(ctx context.Context, id string) (*boosttypes.TransferState, error) {
	return nil, nil
}

func (m *mockLibp2pCarServer) Matching(f httptransport.MatchFn) ([]*httptransport.Libp2pTransfer, error) {
	return nil, nil
}

var _ libp2pCarServer = (*mockLibp2pCarServer)(nil)
