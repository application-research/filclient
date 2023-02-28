package filclient

import (
	"context"
	"fmt"
	"time"

	"github.com/filecoin-project/go-address"
	cborutil "github.com/filecoin-project/go-cbor-util"
	"github.com/filecoin-project/lotus/chain/types"
	inet "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/multiformats/go-multiaddr"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

func (fc *FilClient) streamToMiner(ctx context.Context, maddr address.Address, protocol ...protocol.ID) (inet.Stream, error) {
	ctx, span := Tracer.Start(ctx, "streamToMiner", trace.WithAttributes(
		attribute.Stringer("miner", maddr),
	))
	defer span.End()

	mpid, err := fc.ConnectToMiner(ctx, maddr)
	if err != nil {
		return nil, err
	}

	s, err := fc.host.NewStream(ctx, mpid, protocol...)
	if err != nil {
		return nil, fmt.Errorf("failed to open stream to peer: %w", err)
	}

	return s, nil
}

// Errors - ErrMinerConnectionFailed, ErrLotusError
func (fc *FilClient) ConnectToMiner(ctx context.Context, maddr address.Address) (peer.ID, error) {
	addrInfo, err := fc.minerAddrInfo(ctx, maddr)
	if err != nil {
		return "", err
	}

	if err := fc.host.Connect(ctx, *addrInfo); err != nil {
		return "", NewErrMinerConnectionFailed(err)
	}

	return addrInfo.ID, nil
}

func (fc *FilClient) minerAddrInfo(ctx context.Context, maddr address.Address) (*peer.AddrInfo, error) {
	minfo, err := fc.api.StateMinerInfo(ctx, maddr, types.EmptyTSK)
	if err != nil {
		return nil, NewErrLotusError(err)
	}

	if minfo.PeerId == nil {
		return nil, NewErrMinerConnectionFailed(fmt.Errorf("miner %s has no peer ID set", maddr))
	}

	var maddrs []multiaddr.Multiaddr
	for _, mma := range minfo.Multiaddrs {
		ma, err := multiaddr.NewMultiaddrBytes(mma)
		if err != nil {
			return nil, NewErrMinerConnectionFailed(fmt.Errorf("miner %s had invalid multiaddrs in their info: %w", maddr, err))
		}
		maddrs = append(maddrs, ma)
	}

	// FIXME - lotus-client-proper falls back on the DHT when it has a peerid but no multiaddr
	// filc should do the same
	if len(maddrs) == 0 {
		return nil, NewErrMinerConnectionFailed(fmt.Errorf("miner %s has no multiaddrs set on chain", maddr))
	}

	if err := fc.host.Connect(ctx, peer.AddrInfo{
		ID:    *minfo.PeerId,
		Addrs: maddrs,
	}); err != nil {
		return nil, NewErrMinerConnectionFailed(err)
	}

	return &peer.AddrInfo{
		ID:    *minfo.PeerId,
		Addrs: maddrs,
	}, nil
}

func (fc *FilClient) openStreamToPeer(ctx context.Context, addr peer.AddrInfo, protocol protocol.ID) (inet.Stream, error) {
	ctx, span := Tracer.Start(ctx, "openStreamToPeer", trace.WithAttributes(
		attribute.Stringer("peerID", addr.ID),
	))
	defer span.End()

	err := fc.connectToPeer(ctx, addr)
	if err != nil {
		return nil, err
	}

	s, err := fc.host.NewStream(ctx, addr.ID, protocol)
	if err != nil {
		return nil, fmt.Errorf("failed to open stream to peer: %w", err)
	}

	return s, nil
}

// Errors - ErrMinerConnectionFailed, ErrLotusError
func (fc *FilClient) connectToPeer(ctx context.Context, addr peer.AddrInfo) error {
	if err := fc.host.Connect(ctx, addr); err != nil {
		return NewErrMinerConnectionFailed(err)
	}

	return nil
}

func (fc *FilClient) GetMinerVersion(ctx context.Context, maddr address.Address) (string, error) {
	pid, err := fc.ConnectToMiner(ctx, maddr)
	if err != nil {
		return "", err
	}

	agent, err := fc.host.Peerstore().Get(pid, "AgentVersion")
	if err != nil {
		return "", nil
	}

	return agent.(string), nil
}

func doRpc(ctx context.Context, s inet.Stream, req interface{}, resp interface{}) error {
	dline, ok := ctx.Deadline()
	if ok {
		s.SetDeadline(dline)
		defer s.SetDeadline(time.Time{})
	}

	if err := cborutil.WriteCborRPC(s, req); err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}

	if err := cborutil.ReadCborRPC(s, resp); err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	return nil
}

func (fc *FilClient) MinerPeer(ctx context.Context, miner address.Address) (peer.AddrInfo, error) {
	minfo, err := fc.api.StateMinerInfo(ctx, miner, types.EmptyTSK)
	if err != nil {
		return peer.AddrInfo{}, err
	}

	if minfo.PeerId == nil {
		return peer.AddrInfo{}, fmt.Errorf("miner %s has no peer ID set", miner)
	}

	var maddrs []multiaddr.Multiaddr
	for _, mma := range minfo.Multiaddrs {
		ma, err := multiaddr.NewMultiaddrBytes(mma)
		if err != nil {
			return peer.AddrInfo{}, fmt.Errorf("miner %s had invalid multiaddrs in their info: %w", miner, err)
		}
		maddrs = append(maddrs, ma)
	}

	return peer.AddrInfo{
		ID:    *minfo.PeerId,
		Addrs: maddrs,
	}, nil
}

func (fc *FilClient) minerOwner(ctx context.Context, miner address.Address) (address.Address, error) {
	minfo, err := fc.api.StateMinerInfo(ctx, miner, types.EmptyTSK)
	if err != nil {
		return address.Undef, err
	}
	if minfo.PeerId == nil {
		return address.Undef, fmt.Errorf("miner has no peer id")
	}

	return minfo.Owner, nil
}
