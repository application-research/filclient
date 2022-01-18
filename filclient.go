package filclient

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-commp-utils/writer"
	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/filecoin-project/go-data-transfer/channelmonitor"
	dtimpl "github.com/filecoin-project/go-data-transfer/impl"
	dtnet "github.com/filecoin-project/go-data-transfer/network"
	gst "github.com/filecoin-project/go-data-transfer/transport/graphsync"
	commcid "github.com/filecoin-project/go-fil-commcid"
	"github.com/filecoin-project/go-fil-markets/retrievalmarket"
	"github.com/filecoin-project/go-fil-markets/shared"
	"github.com/filecoin-project/go-fil-markets/storagemarket"
	"github.com/filecoin-project/go-fil-markets/storagemarket/impl/clientutils"
	"github.com/filecoin-project/go-fil-markets/storagemarket/impl/requestvalidation"
	"github.com/filecoin-project/go-fil-markets/storagemarket/network"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/lotus/api"
	"github.com/filecoin-project/lotus/chain/actors/builtin/paych"
	rpcstmgr "github.com/filecoin-project/lotus/chain/stmgr/rpc"
	"github.com/filecoin-project/lotus/chain/types"
	"github.com/filecoin-project/lotus/chain/wallet"
	paychmgr "github.com/filecoin-project/lotus/paychmgr"
	"github.com/filecoin-project/specs-actors/v6/actors/builtin"
	"github.com/filecoin-project/specs-actors/v6/actors/builtin/market"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/namespace"
	"github.com/ipfs/go-graphsync"
	gsimpl "github.com/ipfs/go-graphsync/impl"
	gsnet "github.com/ipfs/go-graphsync/network"
	"github.com/ipfs/go-graphsync/peerstate"
	storeutil "github.com/ipfs/go-graphsync/storeutil"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	logging "github.com/ipfs/go-log/v2"
	car "github.com/ipld/go-car"
	"github.com/libp2p/go-libp2p-core/host"
	inet "github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	protocol "github.com/libp2p/go-libp2p-protocol"
	"github.com/multiformats/go-multiaddr"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	commp "github.com/filecoin-project/go-fil-commp-hashhash"

	cborutil "github.com/filecoin-project/go-cbor-util"
)

var Tracer trace.Tracer = otel.Tracer("filclient")

var log = logging.Logger("filclient")

const DealProtocol = "/fil/storage/mk/1.1.0"
const QueryAskProtocol = "/fil/storage/ask/1.1.0"
const DealStatusProtocol = "/fil/storage/status/1.1.0"
const RetrievalQueryProtocol = "/fil/retrieval/qry/1.0.0"

const maxTraversalLinks = 32 * (1 << 20)

type FilClient struct {
	mpusher *MsgPusher

	pchmgr *paychmgr.Manager

	host host.Host

	api api.Gateway

	wallet *wallet.LocalWallet

	ClientAddr address.Address

	blockstore blockstore.Blockstore

	dataTransfer datatransfer.Manager

	computePieceComm GetPieceCommFunc

	graphSync *gsimpl.GraphSync

	transport *gst.Transport
}

type GetPieceCommFunc func(ctx context.Context, payloadCid cid.Cid, bstore blockstore.Blockstore) (cid.Cid, abi.UnpaddedPieceSize, error)

type Config struct {
	DataDir              string
	GraphsyncOpts        []gsimpl.Option
	Api                  api.Gateway
	Wallet               *wallet.LocalWallet
	Addr                 address.Address
	Blockstore           blockstore.Blockstore
	Datastore            datastore.Batching
	Host                 host.Host
	ChannelMonitorConfig channelmonitor.Config
	RetrievalConfigurer  datatransfer.TransportConfigurer
}

func NewClient(h host.Host, api api.Gateway, w *wallet.LocalWallet, addr address.Address, bs blockstore.Blockstore, ds datastore.Batching, ddir string, opts ...func(*Config)) (*FilClient, error) {

	cfg := &Config{
		Host:       h,
		Api:        api,
		Wallet:     w,
		Addr:       addr,
		Blockstore: bs,
		Datastore:  ds,
		DataDir:    ddir,
		GraphsyncOpts: []gsimpl.Option{
			gsimpl.MaxInProgressIncomingRequests(200),
			gsimpl.MaxInProgressOutgoingRequests(200),
			gsimpl.MaxMemoryResponder(8 << 30),
			gsimpl.MaxMemoryPerPeerResponder(32 << 20),
			gsimpl.MaxInProgressIncomingRequestsPerPeer(20),
			gsimpl.MessageSendRetries(2),
			gsimpl.SendMessageTimeout(2 * time.Minute),
			gsimpl.MaxLinksPerIncomingRequests(maxTraversalLinks),
			gsimpl.MaxLinksPerOutgoingRequests(maxTraversalLinks),
		},
		ChannelMonitorConfig: channelmonitor.Config{

			AcceptTimeout:          time.Hour * 24,
			RestartDebounce:        time.Second * 10,
			RestartBackoff:         time.Second * 20,
			MaxConsecutiveRestarts: 15,
			//RestartAckTimeout:      time.Second * 30,
			CompleteTimeout: time.Minute * 40,

			// Called when a restart completes successfully
			//OnRestartComplete func(id datatransfer.ChannelID)
		},
	}

	for _, opt := range opts {
		opt(cfg)
	}

	return NewClientWithConfig(cfg)
}

func NewClientWithConfig(cfg *Config) (*FilClient, error) {
	ctx, shutdown := context.WithCancel(context.Background())

	mpusher := NewMsgPusher(cfg.Api, cfg.Wallet)

	smapi := rpcstmgr.NewRPCStateManager(cfg.Api)

	pchds := namespace.Wrap(cfg.Datastore, datastore.NewKey("paych"))
	store := paychmgr.NewStore(pchds)

	papi := &paychApiProvider{
		Gateway: cfg.Api,
		wallet:  cfg.Wallet,
		mp:      mpusher,
	}

	pchmgr := paychmgr.NewManager(ctx, shutdown, smapi, store, papi)
	if err := pchmgr.Start(); err != nil {
		return nil, err
	}

	gse := gsimpl.New(context.Background(),
		gsnet.NewFromLibp2pHost(cfg.Host),
		storeutil.LinkSystemForBlockstore(cfg.Blockstore),
		cfg.GraphsyncOpts...,
	).(*gsimpl.GraphSync)

	dtn := dtnet.NewFromLibp2pHost(cfg.Host)
	tpt := gst.NewTransport(cfg.Host.ID(), gse, dtn)

	dtRestartConfig := dtimpl.ChannelRestartConfig(cfg.ChannelMonitorConfig)

	cidlistsdirPath := filepath.Join(cfg.DataDir, "cidlistsdir")
	if err := os.MkdirAll(cidlistsdirPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to initialize cidlistsdir: %w", err)
	}

	mgr, err := dtimpl.NewDataTransfer(cfg.Datastore, cidlistsdirPath, dtn, tpt, dtRestartConfig)
	if err != nil {
		return nil, err
	}

	err = mgr.RegisterVoucherType(&requestvalidation.StorageDataTransferVoucher{}, nil)
	if err != nil {
		return nil, err
	}

	err = mgr.RegisterVoucherType(&retrievalmarket.DealProposal{}, nil)
	if err != nil {
		return nil, err
	}

	err = mgr.RegisterVoucherType(&retrievalmarket.DealPayment{}, nil)
	if err != nil {
		return nil, err
	}

	err = mgr.RegisterVoucherResultType(&retrievalmarket.DealResponse{})
	if err != nil {
		return nil, err
	}

	if cfg.RetrievalConfigurer != nil {
		if err := mgr.RegisterTransportConfigurer(&retrievalmarket.DealProposal{}, cfg.RetrievalConfigurer); err != nil {
			return nil, err
		}
	}

	if err := mgr.Start(context.TODO()); err != nil {
		return nil, err
	}

	/*
		mgr.SubscribeToEvents(func(event datatransfer.Event, channelState datatransfer.ChannelState) {
			fmt.Printf("[%s] event: %s %d %s %s\n", time.Now().Format("15:04:05"), event.Message, event.Code, datatransfer.Events[event.Code], event.Timestamp.Format("15:04:05"))
			fmt.Printf("channelstate: %s %s\n", datatransfer.Statuses[channelState.Status()], channelState.Message())
		})
	*/

	return &FilClient{
		host:             cfg.Host,
		api:              cfg.Api,
		wallet:           cfg.Wallet,
		ClientAddr:       cfg.Addr,
		blockstore:       cfg.Blockstore,
		dataTransfer:     mgr,
		pchmgr:           pchmgr,
		mpusher:          mpusher,
		computePieceComm: GeneratePieceCommitment,
		graphSync:        gse,
		transport:        tpt,
	}, nil
}

func (fc *FilClient) SetPieceCommFunc(pcf GetPieceCommFunc) {
	fc.computePieceComm = pcf
}

func (fc *FilClient) streamToMiner(ctx context.Context, maddr address.Address, protocol protocol.ID) (inet.Stream, error) {
	ctx, span := Tracer.Start(ctx, "streamToMiner", trace.WithAttributes(
		attribute.Stringer("miner", maddr),
	))
	defer span.End()

	mpid, err := fc.connectToMiner(ctx, maddr)
	if err != nil {
		return nil, err
	}

	s, err := fc.host.NewStream(ctx, mpid, protocol)
	if err != nil {
		return nil, fmt.Errorf("failed to open stream to peer: %w", err)
	}

	return s, nil
}

// Errors - ErrMinerConnectionFailed, ErrLotusError
func (fc *FilClient) connectToMiner(ctx context.Context, maddr address.Address) (peer.ID, error) {
	minfo, err := fc.api.StateMinerInfo(ctx, maddr, types.EmptyTSK)
	if err != nil {
		return "", NewErrLotusError(err)
	}

	if minfo.PeerId == nil {
		return "", NewErrMinerConnectionFailed(fmt.Errorf("miner %s has no peer ID set", maddr))
	}

	var maddrs []multiaddr.Multiaddr
	for _, mma := range minfo.Multiaddrs {
		ma, err := multiaddr.NewMultiaddrBytes(mma)
		if err != nil {
			return "", NewErrMinerConnectionFailed(fmt.Errorf("miner %s had invalid multiaddrs in their info: %w", maddr, err))
		}
		maddrs = append(maddrs, ma)
	}

	// FIXME - lotus-client-proper falls back on the DHT when it has a peerid but no multiaddr
	// filc should do the same
	if len(maddrs) == 0 {
		return "", NewErrMinerConnectionFailed(fmt.Errorf("miner %s has no multiaddrs set on chain", maddr))
	}

	if err := fc.host.Connect(ctx, peer.AddrInfo{
		ID:    *minfo.PeerId,
		Addrs: maddrs,
	}); err != nil {
		return "", NewErrMinerConnectionFailed(err)
	}

	return *minfo.PeerId, nil
}

func (fc *FilClient) GetMinerVersion(ctx context.Context, maddr address.Address) (string, error) {
	pid, err := fc.connectToMiner(ctx, maddr)
	if err != nil {
		return "", err
	}

	agent, err := fc.host.Peerstore().Get(pid, "AgentVersion")
	if err != nil {
		return "", nil
	}

	return agent.(string), nil
}

func (fc *FilClient) GetAsk(ctx context.Context, maddr address.Address) (*network.AskResponse, error) {
	ctx, span := Tracer.Start(ctx, "doGetAsk", trace.WithAttributes(
		attribute.Stringer("miner", maddr),
	))
	defer span.End()

	s, err := fc.streamToMiner(ctx, maddr, QueryAskProtocol)
	if err != nil {
		return nil, err
	}
	defer s.Close()

	areq := &network.AskRequest{maddr}
	var resp network.AskResponse
	if err := doRpc(ctx, s, areq, &resp); err != nil {
		return nil, fmt.Errorf("get ask rpc: %w", err)
	}

	return &resp, nil
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

const epochsPerHour = 60 * 2

func ComputePrice(askPrice types.BigInt, size abi.PaddedPieceSize, duration abi.ChainEpoch) (*abi.TokenAmount, error) {
	cost := big.Mul(big.Div(big.Mul(big.NewInt(int64(size)), askPrice), big.NewInt(1<<30)), big.NewInt(int64(duration)))

	return (*abi.TokenAmount)(&cost), nil
}

func (fc *FilClient) MakeDeal(ctx context.Context, miner address.Address, data cid.Cid, price types.BigInt, minSize abi.PaddedPieceSize, duration abi.ChainEpoch, verified bool) (*network.Proposal, error) {
	ctx, span := Tracer.Start(ctx, "makeDeal", trace.WithAttributes(
		attribute.Stringer("miner", miner),
		attribute.Stringer("price", price),
		attribute.Int64("minSize", int64(minSize)),
		attribute.Int64("duration", int64(duration)),
		attribute.Stringer("cid", data),
	))
	defer span.End()

	commP, size, err := fc.computePieceComm(ctx, data, fc.blockstore)
	if err != nil {
		return nil, err
	}

	if size.Padded() < minSize {
		padded, err := ZeroPadPieceCommitment(commP, size, minSize.Unpadded())
		if err != nil {
			return nil, err
		}

		commP = padded
		size = minSize.Unpadded()
	}

	head, err := fc.api.ChainHead(ctx)
	if err != nil {
		return nil, err
	}

	collBounds, err := fc.api.StateDealProviderCollateralBounds(ctx, size.Padded(), verified, types.EmptyTSK)
	if err != nil {
		return nil, err
	}

	// set provider collateral 10% above minimum to avoid fluctuations causing deal failure
	provCol := big.Div(big.Mul(collBounds.Min, big.NewInt(11)), big.NewInt(10))

	// give miners a week to seal and commit the sector
	dealStart := head.Height() + (epochsPerHour * 24 * 7)

	end := dealStart + duration

	pricePerEpoch := big.Div(big.Mul(big.NewInt(int64(size.Padded())), price), big.NewInt(1<<30))

	label, err := clientutils.LabelField(data)
	if err != nil {
		return nil, fmt.Errorf("failed to construct label field: %w", err)
	}

	proposal := &market.DealProposal{
		PieceCID:     commP,
		PieceSize:    size.Padded(),
		VerifiedDeal: verified,
		Client:       fc.ClientAddr,
		Provider:     miner,

		Label: label,

		StartEpoch: dealStart,
		EndEpoch:   end,

		StoragePricePerEpoch: pricePerEpoch,
		ProviderCollateral:   provCol,
		ClientCollateral:     big.Zero(),
	}

	raw, err := cborutil.Dump(proposal)
	if err != nil {
		return nil, err
	}
	sig, err := fc.wallet.WalletSign(ctx, fc.ClientAddr, raw, api.MsgMeta{Type: api.MTDealProposal})
	if err != nil {
		return nil, err
	}

	sigprop := &market.ClientDealProposal{
		Proposal:        *proposal,
		ClientSignature: *sig,
	}

	return &network.Proposal{
		DealProposal: sigprop,
		Piece: &storagemarket.DataRef{
			TransferType: storagemarket.TTGraphsync,
			Root:         data,
		},
		FastRetrieval: true,
	}, nil
}

func (fc *FilClient) SendProposal(ctx context.Context, netprop *network.Proposal) (*network.SignedResponse, error) {
	ctx, span := Tracer.Start(ctx, "sendProposal")
	defer span.End()

	s, err := fc.streamToMiner(ctx, netprop.DealProposal.Proposal.Provider, DealProtocol)
	if err != nil {
		return nil, fmt.Errorf("opening stream to miner: %w", err)
	}

	defer s.Close()

	var resp network.SignedResponse

	if err := doRpc(ctx, s, netprop, &resp); err != nil {
		return nil, fmt.Errorf("send proposal rpc: %w", err)
	}

	return &resp, nil
}

func GeneratePieceCommitment(ctx context.Context, payloadCid cid.Cid, bstore blockstore.Blockstore) (cid.Cid, abi.UnpaddedPieceSize, error) {
	selectiveCar := car.NewSelectiveCar(
		context.Background(),
		bstore,
		[]car.Dag{{Root: payloadCid, Selector: shared.AllSelector()}},
		car.MaxTraversalLinks(maxTraversalLinks),
		car.TraverseLinksOnlyOnce(),
	)
	preparedCar, err := selectiveCar.Prepare()
	if err != nil {
		return cid.Undef, 0, err
	}

	writer := new(commp.Calc)
	err = preparedCar.Dump(ctx, writer)
	if err != nil {
		return cid.Undef, 0, err
	}

	commpc, size, err := writer.Digest()
	if err != nil {
		return cid.Undef, 0, err
	}

	commCid, err := commcid.DataCommitmentV1ToCID(commpc)
	if err != nil {
		return cid.Undef, 0, err
	}

	return commCid, abi.PaddedPieceSize(size).Unpadded(), nil
}

func GeneratePieceCommitmentFFI(ctx context.Context, payloadCid cid.Cid, bstore blockstore.Blockstore) (cid.Cid, abi.UnpaddedPieceSize, error) {
	selectiveCar := car.NewSelectiveCar(
		context.Background(),
		bstore,
		[]car.Dag{{Root: payloadCid, Selector: shared.AllSelector()}},
		car.MaxTraversalLinks(maxTraversalLinks),
		car.TraverseLinksOnlyOnce(),
	)
	preparedCar, err := selectiveCar.Prepare()
	if err != nil {
		return cid.Undef, 0, err
	}

	commpWriter := &writer.Writer{}
	err = preparedCar.Dump(ctx, commpWriter)
	if err != nil {
		return cid.Undef, 0, err
	}

	dataCIDSize, err := commpWriter.Sum()
	if err != nil {
		return cid.Undef, 0, err
	}

	return dataCIDSize.PieceCID, dataCIDSize.PieceSize.Unpadded(), nil
}

func ZeroPadPieceCommitment(c cid.Cid, curSize abi.UnpaddedPieceSize, toSize abi.UnpaddedPieceSize) (cid.Cid, error) {

	rawPaddedCommp, err := commp.PadCommP(
		// we know how long a pieceCid "hash" is, just blindly extract the trailing 32 bytes
		c.Hash()[len(c.Hash())-32:],
		uint64(curSize.Padded()),
		uint64(toSize.Padded()),
	)
	if err != nil {
		return cid.Undef, err
	}
	return commcid.DataCommitmentV1ToCID(rawPaddedCommp)
}

func (fc *FilClient) DealStatus(ctx context.Context, miner address.Address, propCid cid.Cid) (*storagemarket.ProviderDealState, error) {
	cidb, err := cborutil.Dump(propCid)
	if err != nil {
		return nil, err
	}

	sig, err := fc.wallet.WalletSign(ctx, fc.ClientAddr, cidb, api.MsgMeta{Type: api.MTUnknown})
	if err != nil {
		return nil, fmt.Errorf("signing status request failed: %w", err)
	}

	req := &network.DealStatusRequest{
		Proposal:  propCid,
		Signature: *sig,
	}

	s, err := fc.streamToMiner(ctx, miner, DealStatusProtocol)
	if err != nil {
		return nil, err
	}

	var resp network.DealStatusResponse
	if err := doRpc(ctx, s, req, &resp); err != nil {
		return nil, fmt.Errorf("deal status rpc: %w", err)
	}

	// TODO: check the signatures and stuff?

	return &resp.DealState, nil
}

func (fc *FilClient) minerPeer(ctx context.Context, miner address.Address) (peer.ID, error) {
	minfo, err := fc.api.StateMinerInfo(ctx, miner, types.EmptyTSK)
	if err != nil {
		return "", err
	}
	if minfo.PeerId == nil {
		return "", fmt.Errorf("miner has no peer id")
	}

	return *minfo.PeerId, nil
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

type ChannelState struct {
	//datatransfer.Channel

	// SelfPeer returns the peer this channel belongs to
	SelfPeer   peer.ID `json:"selfPeer"`
	RemotePeer peer.ID `json:"remotePeer"`

	// Status is the current status of this channel
	Status    datatransfer.Status `json:"status"`
	StatusStr string              `json:"statusMessage"`

	// Sent returns the number of bytes sent
	Sent uint64 `json:"sent"`

	// Received returns the number of bytes received
	Received uint64 `json:"received"`

	// Message offers additional information about the current status
	Message string `json:"message"`

	BaseCid string `json:"baseCid"`

	ChannelID datatransfer.ChannelID `json:"channelId"`

	// Vouchers returns all vouchers sent on this channel
	//Vouchers []datatransfer.Voucher

	// VoucherResults are results of vouchers sent on the channel
	//VoucherResults []datatransfer.VoucherResult

	// LastVoucher returns the last voucher sent on the channel
	//LastVoucher datatransfer.Voucher

	// LastVoucherResult returns the last voucher result sent on the channel
	//LastVoucherResult datatransfer.VoucherResult

	// ReceivedCids returns the cids received so far on the channel
	//ReceivedCids []cid.Cid

	// Queued returns the number of bytes read from the node and queued for sending
	//Queued uint64
}

func ChannelStateConv(st datatransfer.ChannelState) *ChannelState {
	return &ChannelState{
		SelfPeer:   st.SelfPeer(),
		RemotePeer: st.OtherPeer(),
		Status:     st.Status(),
		StatusStr:  datatransfer.Statuses[st.Status()],
		Sent:       st.Sent(),
		Received:   st.Received(),
		Message:    st.Message(),
		BaseCid:    st.BaseCID().String(),
		ChannelID:  st.ChannelID(),
		//Vouchers:          st.Vouchers(),
		//VoucherResults:    st.VoucherResults(),
		//LastVoucher:       st.LastVoucher(),
		//LastVoucherResult: st.LastVoucherResult(),
		//ReceivedCids:      st.ReceivedCids(),
		//Queued:            st.Queued(),
	}
}

func (fc *FilClient) TransfersInProgress(ctx context.Context) (map[datatransfer.ChannelID]datatransfer.ChannelState, error) {
	return fc.dataTransfer.InProgressChannels(ctx)
}

type GraphSyncDataTransfer struct {
	// GraphSync request id for this transfer
	RequestID graphsync.RequestID `json:"requestID"`
	// Graphsync state for this transfer
	RequestState string `json:"requestState"`
	// If a channel ID is present, indicates whether this is the current graphsync request for this channel
	// (could have changed in a restart)
	IsCurrentChannelRequest bool `json:"isCurrentChannelRequest"`
	// Data transfer channel ID for this transfer
	ChannelID *datatransfer.ChannelID `json:"channelID"`
	// Data transfer state for this transfer
	ChannelState *ChannelState `json:"channelState"`
	// Diagnostic information about this request -- and unexpected inconsistencies in
	// request state
	Diagnostics []string `json:"diagnostics"`
}

type MinerTransferDiagnostics struct {
	ReceivingTransfers []*GraphSyncDataTransfer `json:"sendingTransfers"`
	SendingTransfers   []*GraphSyncDataTransfer `json:"receivingTransfers"`
}

// MinerTransferDiagnostics provides in depth current information on the state of transfers for a given miner,
// covering running graphsync requests and related data transfers, etc
func (fc *FilClient) MinerTransferDiagnostics(ctx context.Context, miner address.Address) (*MinerTransferDiagnostics, error) {
	start := time.Now()
	defer func() {
		log.Infof("get miner diagnostics took: %s", time.Since(start))
	}()
	mpid, err := fc.minerPeer(ctx, miner)
	if err != nil {
		return nil, err
	}
	// gather information about active transport channels
	transportChannels := fc.transport.ChannelsForPeer(mpid)
	// gather information about graphsync state for peer
	gsPeerState := fc.graphSync.PeerState(mpid)

	sendingTransfers := fc.generateTransfers(ctx, transportChannels.SendingChannels, gsPeerState.IncomingState)
	receivingTransfers := fc.generateTransfers(ctx, transportChannels.ReceivingChannels, gsPeerState.OutgoingState)

	return &MinerTransferDiagnostics{
		SendingTransfers:   sendingTransfers,
		ReceivingTransfers: receivingTransfers,
	}, nil
}

// generate transfers matches graphsync state and data transfer state for a given peer
// to produce detailed output on what's happening with a transfer
func (fc *FilClient) generateTransfers(ctx context.Context,
	transportChannels map[datatransfer.ChannelID]gst.ChannelGraphsyncRequests,
	gsPeerState peerstate.PeerState) []*GraphSyncDataTransfer {
	tc := &transferConverter{
		matchedRequests: make(map[graphsync.RequestID]*GraphSyncDataTransfer),
		gsDiagnostics:   gsPeerState.Diagnostics(),
		requestStates:   gsPeerState.RequestStates,
	}

	// iterate through all operating data transfer transport channels
	for channelID, channelRequests := range transportChannels {
		channelState, err := fc.TransferStatus(ctx, &channelID)
		var baseDiagnostics []string
		if err != nil {
			baseDiagnostics = append(baseDiagnostics, fmt.Sprintf("Unable to lookup channel state: %s", err))
			channelState = nil
		}
		// add the current request for this channel
		tc.convertTransfer(&channelID, channelState, baseDiagnostics, channelRequests.Current, true)
		for _, requestID := range channelRequests.Previous {
			// add any previous requests that were cancelled for a restart
			tc.convertTransfer(&channelID, channelState, baseDiagnostics, requestID, false)
		}
	}

	// collect any graphsync data for channels we don't have any data transfer data for
	tc.collectRemainingTransfers()

	return tc.transfers
}

type transferConverter struct {
	matchedRequests map[graphsync.RequestID]*GraphSyncDataTransfer
	transfers       []*GraphSyncDataTransfer
	gsDiagnostics   map[graphsync.RequestID][]string
	requestStates   graphsync.RequestStates
}

// convert transfer assembles transfer and diagnostic data for a given graphsync/data-transfer request
func (tc *transferConverter) convertTransfer(channelID *datatransfer.ChannelID, channelState *ChannelState, baseDiagnostics []string,
	requestID graphsync.RequestID, isCurrentChannelRequest bool) {
	diagnostics := baseDiagnostics
	state, hasState := tc.requestStates[requestID]
	stateString := state.String()
	if !hasState {
		stateString = "no graphsync state found"
	}
	if channelID == nil {
		diagnostics = append(diagnostics, fmt.Sprintf("No data transfer channel id for GraphSync request ID %d", requestID))
	} else if !hasState {
		diagnostics = append(diagnostics, fmt.Sprintf("No current request state for data transfer channel id %s", channelID))
	}
	diagnostics = append(diagnostics, tc.gsDiagnostics[requestID]...)
	transfer := &GraphSyncDataTransfer{
		RequestID:               requestID,
		RequestState:            stateString,
		IsCurrentChannelRequest: isCurrentChannelRequest,
		ChannelID:               channelID,
		ChannelState:            channelState,
		Diagnostics:             diagnostics,
	}
	tc.transfers = append(tc.transfers, transfer)
	tc.matchedRequests[requestID] = transfer
}

func (tc *transferConverter) collectRemainingTransfers() {
	for requestID := range tc.requestStates {
		if _, ok := tc.matchedRequests[requestID]; !ok {
			tc.convertTransfer(nil, nil, nil, requestID, false)
		}
	}
	for requestID := range tc.gsDiagnostics {
		if _, ok := tc.matchedRequests[requestID]; !ok {
			tc.convertTransfer(nil, nil, nil, requestID, false)
		}
	}
}

func (fc *FilClient) GraphSyncStats() graphsync.Stats {
	return fc.graphSync.Stats()
}

func (fc *FilClient) TransferStatus(ctx context.Context, chanid *datatransfer.ChannelID) (*ChannelState, error) {
	st, err := fc.dataTransfer.ChannelState(ctx, *chanid)
	if err != nil {
		return nil, err
	}

	return ChannelStateConv(st), nil
}

var ErrNoTransferFound = fmt.Errorf("no transfer found")

func (fc *FilClient) TransferStatusForContent(ctx context.Context, content cid.Cid, miner address.Address) (*ChannelState, error) {
	start := time.Now()
	defer func() {
		log.Infof("check transfer status took: %s", time.Since(start))
	}()
	mpid, err := fc.minerPeer(ctx, miner)
	if err != nil {
		return nil, err
	}

	inprog, err := fc.dataTransfer.InProgressChannels(ctx)
	if err != nil {
		return nil, err
	}

	for chanid, state := range inprog {
		if chanid.Responder == mpid {
			if state.IsPull() {
				// this isnt a storage deal transfer...
				continue
			}
			if state.BaseCID() == content {
				return ChannelStateConv(state), nil
			}
		}
	}

	return nil, ErrNoTransferFound
}

func (fc *FilClient) RestartTransfer(ctx context.Context, chanid *datatransfer.ChannelID) error {
	return fc.dataTransfer.RestartDataTransferChannel(ctx, *chanid)
}

func (fc *FilClient) StartDataTransfer(ctx context.Context, miner address.Address, propCid cid.Cid, dataCid cid.Cid) (*datatransfer.ChannelID, error) {
	ctx, span := Tracer.Start(ctx, "startDataTransfer")
	defer span.End()

	mpid, err := fc.connectToMiner(ctx, miner)
	if err != nil {
		return nil, err
	}

	voucher := &requestvalidation.StorageDataTransferVoucher{Proposal: propCid}

	fc.host.ConnManager().Protect(mpid, "transferring")

	chanid, err := fc.dataTransfer.OpenPushDataChannel(ctx, mpid, voucher, dataCid, shared.AllSelector())
	if err != nil {
		return nil, fmt.Errorf("opening push data channel: %w", err)
	}

	return &chanid, nil
}

func (fc *FilClient) SubscribeToDataTransferEvents(f datatransfer.Subscriber) func() {
	return fc.dataTransfer.SubscribeToEvents(f)
}

type Balance struct {
	Account         address.Address `json:"account"`
	Balance         types.FIL       `json:"balance"`
	MarketEscrow    types.FIL       `json:"marketEscrow"`
	MarketLocked    types.FIL       `json:"marketLocked"`
	MarketAvailable types.FIL       `json:"marketAvailable"`

	VerifiedClientBalance *abi.StoragePower `json:"verifiedClientBalance"`
}

func (fc *FilClient) Balance(ctx context.Context) (*Balance, error) {
	act, err := fc.api.StateGetActor(ctx, fc.ClientAddr, types.EmptyTSK)
	if err != nil {
		return nil, err
	}

	market, err := fc.api.StateMarketBalance(ctx, fc.ClientAddr, types.EmptyTSK)
	if err != nil {
		return nil, err
	}

	vcstatus, err := fc.api.StateVerifiedClientStatus(ctx, fc.ClientAddr, types.EmptyTSK)
	if err != nil {
		return nil, err
	}

	avail := types.BigSub(market.Escrow, market.Locked)

	return &Balance{
		Account:               fc.ClientAddr,
		Balance:               types.FIL(act.Balance),
		MarketEscrow:          types.FIL(market.Escrow),
		MarketLocked:          types.FIL(market.Locked),
		MarketAvailable:       types.FIL(avail),
		VerifiedClientBalance: vcstatus,
	}, nil
}

type LockFundsResp struct {
	MsgCid cid.Cid
}

func (fc *FilClient) LockMarketFunds(ctx context.Context, amt types.FIL) (*LockFundsResp, error) {

	act, err := fc.api.StateGetActor(ctx, fc.ClientAddr, types.EmptyTSK)
	if err != nil {
		return nil, err
	}

	if types.BigCmp(types.BigInt(amt), act.Balance) > 0 {
		return nil, fmt.Errorf("not enough funds to add: %s < %s", types.FIL(act.Balance), amt)
	}

	encAddr, err := cborutil.Dump(&fc.ClientAddr)
	if err != nil {
		return nil, err
	}

	msg := &types.Message{
		From:   fc.ClientAddr,
		To:     builtin.StorageMarketActorAddr,
		Method: builtin.MethodsMarket.AddBalance,
		Value:  types.BigInt(amt),
		Params: encAddr,
		Nonce:  act.Nonce,
	}

	smsg, err := fc.mpusher.MpoolPushMessage(ctx, msg, &api.MessageSendSpec{})
	if err != nil {
		return nil, err
	}

	return &LockFundsResp{
		MsgCid: smsg.Cid(),
	}, nil
}

func (fc *FilClient) CheckChainDeal(ctx context.Context, dealid abi.DealID) (bool, *api.MarketDeal, error) {
	deal, err := fc.api.StateMarketStorageDeal(ctx, dealid, types.EmptyTSK)
	if err != nil {
		nfs := fmt.Sprintf("deal %d not found", dealid)
		if strings.Contains(err.Error(), nfs) {
			return false, nil, nil
		}

		return false, nil, err
	}

	return true, deal, nil
}

func (fc *FilClient) CheckOngoingTransfer(ctx context.Context, miner address.Address, st *ChannelState) (outerr error) {
	defer func() {
		// TODO: this is only here because for some reason restarting a data transfer can just panic
		// https://github.com/filecoin-project/go-data-transfer/issues/150
		if e := recover(); e != nil {
			outerr = fmt.Errorf("panic while checking transfer: %s", e)
		}
	}()
	// make sure we at least have an open connection to the miner
	if fc.host.Network().Connectedness(st.RemotePeer) != inet.Connected {
		// try reconnecting
		mpid, err := fc.connectToMiner(ctx, miner)
		if err != nil {
			return err
		}

		if mpid != st.RemotePeer {
			return fmt.Errorf("miner peer ID is different than RemotePeer in data transfer channel")
		}
	}

	return fc.dataTransfer.RestartDataTransferChannel(ctx, st.ChannelID)

}

func (fc *FilClient) RetrievalQuery(ctx context.Context, maddr address.Address, pcid cid.Cid) (*retrievalmarket.QueryResponse, error) {
	ctx, span := Tracer.Start(ctx, "retrievalQuery", trace.WithAttributes(
		attribute.Stringer("miner", maddr),
	))
	defer span.End()

	s, err := fc.streamToMiner(ctx, maddr, RetrievalQueryProtocol)
	if err != nil {
		return nil, err
	}

	q := &retrievalmarket.Query{
		PayloadCID: pcid,
	}

	var resp retrievalmarket.QueryResponse
	if err := doRpc(ctx, s, q, &resp); err != nil {
		return nil, fmt.Errorf("retrieval query rpc: %w", err)
	}

	return &resp, nil
}

func (fc *FilClient) getPaychWithMinFunds(ctx context.Context, dest address.Address) (address.Address, error) {

	avail, err := fc.pchmgr.AvailableFundsByFromTo(ctx, fc.ClientAddr, dest)
	if err != nil {
		return address.Undef, err
	}

	reqBalance, err := types.ParseFIL("0.01")
	if err != nil {
		return address.Undef, err
	}
	fmt.Println("available", avail.ConfirmedAmt)

	if types.BigCmp(avail.ConfirmedAmt, types.BigInt(reqBalance)) >= 0 {
		return *avail.Channel, nil
	}

	amount := abi.TokenAmount(types.BigMul(types.BigInt(reqBalance), types.NewInt(2)))

	fmt.Println("getting payment channel: ", fc.ClientAddr, dest, amount)
	pchaddr, mcid, err := fc.pchmgr.GetPaych(ctx, fc.ClientAddr, dest, amount)
	if err != nil {
		return address.Undef, fmt.Errorf("failed to get payment channel: %w", err)
	}

	fmt.Println("got payment channel: ", pchaddr, mcid)
	if !mcid.Defined() {
		if pchaddr == address.Undef {
			return address.Undef, fmt.Errorf("GetPaych returned nothing")
		}

		return pchaddr, nil
	}

	return fc.pchmgr.GetPaychWaitReady(ctx, mcid)
}

type RetrievalStats struct {
	Peer         peer.ID
	Size         uint64
	Duration     time.Duration
	AverageSpeed uint64
	TotalPayment abi.TokenAmount
	NumPayments  int
	AskPrice     abi.TokenAmount

	// TODO: we should be able to get this if we hook into the graphsync event stream
	//TimeToFirstByte time.Duration
}

func (fc *FilClient) RetrieveContent(
	ctx context.Context,
	miner address.Address,
	proposal *retrievalmarket.DealProposal,
) (*RetrievalStats, error) {
	return fc.RetrieveContentWithProgressCallback(ctx, miner, proposal, nil)
}

func (fc *FilClient) RetrieveContentWithProgressCallback(
	ctx context.Context,
	miner address.Address,
	proposal *retrievalmarket.DealProposal,
	progressCallback func(bytesReceived uint64),
) (*RetrievalStats, error) {

	if progressCallback == nil {
		progressCallback = func(bytesReceived uint64) {}
	}

	log.Infof("Starting retrieval with miner: %s", miner)

	ctx, span := Tracer.Start(ctx, "fcRetrieveContent")
	defer span.End()

	// Stats
	startTime := time.Now()
	totalPayment := abi.NewTokenAmount(0)

	mpID, err := fc.minerPeer(ctx, miner)
	if err != nil {
		return nil, fmt.Errorf("failed to get miner peer: %w", err)
	}

	minerOwner, err := fc.minerOwner(ctx, miner)
	if err != nil {
		return nil, err
	}

	pchRequired := !proposal.PricePerByte.IsZero() || !proposal.UnsealPrice.IsZero()
	var pchAddr address.Address
	var pchLane uint64
	if pchRequired {
		// Get the payment channel and create a lane for this retrieval
		pchAddr, err = fc.getPaychWithMinFunds(ctx, minerOwner)
		if err != nil {
			return nil, fmt.Errorf("failed to get payment channel: %w", err)
		}
		pchLane, err = fc.pchmgr.AllocateLane(ctx, pchAddr)
		if err != nil {
			return nil, fmt.Errorf("failed to allocate lane: %w", err)
		}
	}

	var chanid datatransfer.ChannelID
	var chanidLk sync.Mutex

	// Set up incoming events handler

	// The next nonce (incrementing unique ID starting from 0) for the next voucher
	var nonce uint64 = 0

	// dtRes receives either an error (failure) or nil (success) which is waited
	// on and handled below before exiting the function
	dtRes := make(chan error, 1)

	finish := func(err error) {
		select {
		case dtRes <- err:
		default:
		}
	}

	unsubscribe := fc.dataTransfer.SubscribeToEvents(func(event datatransfer.Event, state datatransfer.ChannelState) {
		// Copy chanid so it can be used later in the callback
		chanidLk.Lock()
		chanidCopy := chanid
		chanidLk.Unlock()

		// Skip all events that aren't related to this channel
		if state.ChannelID() != chanidCopy {
			return
		}

		silenceEventCode := false
		eventCodeNotHandled := false

		switch event.Code {
		case datatransfer.Open:
		case datatransfer.Accept:
		case datatransfer.Restart:
		case datatransfer.DataReceived:
			silenceEventCode = true
		case datatransfer.DataSent:
		case datatransfer.Cancel:
		case datatransfer.Error:
		case datatransfer.CleanupComplete:
		case datatransfer.NewVoucher:
		case datatransfer.NewVoucherResult:

			switch resType := state.LastVoucherResult().(type) {
			case *retrievalmarket.DealResponse:
				if len(resType.Message) != 0 {
					log.Debugf("Received deal response voucher result %s (%v): %s\n\t%+v", resType.Status, resType.Status, resType.Message, resType)
				} else {
					log.Debugf("Received deal response voucher result %s (%v)\n\t%+v", resType.Status, resType.Status, resType)
				}

				switch resType.Status {
				case retrievalmarket.DealStatusAccepted:
					log.Info("Deal accepted")

				// Respond with a payment voucher when funds are requested
				case retrievalmarket.DealStatusFundsNeeded:
					if pchRequired {
						log.Infof("Sending payment voucher (nonce: %v, amount: %v)", nonce, resType.PaymentOwed)

						totalPayment = types.BigAdd(totalPayment, resType.PaymentOwed)

						vres, err := fc.pchmgr.CreateVoucher(ctx, pchAddr, paych.SignedVoucher{
							ChannelAddr: pchAddr,
							Lane:        pchLane,
							Nonce:       nonce,
							Amount:      totalPayment,
						})
						if err != nil {
							finish(err)
						}

						if types.BigCmp(vres.Shortfall, big.NewInt(0)) > 0 {
							finish(fmt.Errorf("not enough funds remaining in payment channel (shortfall = %s)", vres.Shortfall))
						}

						if err := fc.dataTransfer.SendVoucher(ctx, chanidCopy, &retrievalmarket.DealPayment{
							ID:             proposal.ID,
							PaymentChannel: pchAddr,
							PaymentVoucher: vres.Voucher,
						}); err != nil {
							finish(fmt.Errorf("failed to send payment voucher: %w", err))
						}

						nonce++
					} else {
						finish(fmt.Errorf("the miner requested payment even though this transaction was determined to be zero cost"))
					}
				case retrievalmarket.DealStatusRejected:
					finish(fmt.Errorf("deal rejected: %s", resType.Message))
				case retrievalmarket.DealStatusFundsNeededUnseal, retrievalmarket.DealStatusUnsealing:
					finish(fmt.Errorf("data is sealed"))
				case retrievalmarket.DealStatusCancelled:
					finish(fmt.Errorf("deal cancelled: %s", resType.Message))
				case retrievalmarket.DealStatusErrored:
					finish(fmt.Errorf("deal errored: %s", resType.Message))
				case retrievalmarket.DealStatusCompleted:
					finish(nil)
				}
			}
		case datatransfer.PauseInitiator:
		case datatransfer.ResumeInitiator:
		case datatransfer.PauseResponder:
		case datatransfer.ResumeResponder:
		case datatransfer.FinishTransfer:
		case datatransfer.ResponderCompletes:
		case datatransfer.ResponderBeginsFinalization:
		case datatransfer.BeginFinalizing:
		case datatransfer.Disconnected:
		case datatransfer.Complete:
		case datatransfer.CompleteCleanupOnRestart:
		case datatransfer.DataQueued:
		case datatransfer.DataQueuedProgress:
		case datatransfer.DataSentProgress:
		case datatransfer.DataReceivedProgress:
			progressCallback(state.Received())
			silenceEventCode = true
		case datatransfer.RequestTimedOut:
		case datatransfer.SendDataError:
		case datatransfer.ReceiveDataError:
		case datatransfer.TransferRequestQueued:
		case datatransfer.RequestCancelled:
		case datatransfer.Opened:
		default:
			eventCodeNotHandled = true
		}

		var eventString string
		name := datatransfer.Events[event.Code]
		code := event.Code
		msg := event.Message
		if len(event.Message) != 0 {
			eventString = fmt.Sprintf("\"%s\" (%v): %s", name, code, msg)
		} else {
			eventString = fmt.Sprintf("\"%s\" (%v)", name, code)
		}

		if eventCodeNotHandled {
			log.Warnf("Unhandled event %s", eventString)
		} else {
			if !silenceEventCode {
				log.Debugf("Processed event %s", eventString)
			}
		}
	})
	defer unsubscribe()

	// Submit the retrieval deal proposal to the miner
	newchid, err := fc.dataTransfer.OpenPullDataChannel(ctx, mpID, proposal, proposal.PayloadCID, shared.AllSelector())
	if err != nil {
		return nil, err
	}

	chanidLk.Lock()
	chanid = newchid
	chanidLk.Unlock()

	defer fc.dataTransfer.CloseDataTransferChannel(ctx, chanid)

	// Wait for the retrieval to finish before exiting the function
	select {
	case err := <-dtRes:
		if err != nil {
			return nil, fmt.Errorf("data transfer failed: %w", err)
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// Compile the retrieval stats

	state, err := fc.dataTransfer.ChannelState(ctx, chanid)
	if err != nil {
		return nil, fmt.Errorf("could not get channel state: %w", err)
	}

	duration := time.Since(startTime)
	speed := uint64(float64(state.Received()) / duration.Seconds())

	return &RetrievalStats{
		Peer:         state.OtherPeer(),
		Size:         state.Received(),
		Duration:     duration,
		AverageSpeed: speed,
		TotalPayment: totalPayment,
		NumPayments:  int(nonce),
		AskPrice:     proposal.PricePerByte,
	}, nil
}
