package filclient

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/application-research/filclient/rep"
	boostcar "github.com/filecoin-project/boost/car"
	"github.com/filecoin-project/boost/transport/httptransport"
	"github.com/filecoin-project/go-address"
	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/filecoin-project/go-data-transfer/channelmonitor"
	dtimpl "github.com/filecoin-project/go-data-transfer/impl"
	dtnet "github.com/filecoin-project/go-data-transfer/network"
	gst "github.com/filecoin-project/go-data-transfer/transport/graphsync"
	"github.com/filecoin-project/go-fil-markets/retrievalmarket"
	"github.com/filecoin-project/go-fil-markets/storagemarket/impl/requestvalidation"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/lotus/api"
	rpcstmgr "github.com/filecoin-project/lotus/chain/stmgr/rpc"
	"github.com/filecoin-project/lotus/chain/types"
	"github.com/filecoin-project/lotus/chain/wallet"
	"github.com/filecoin-project/lotus/paychmgr"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/namespace"
	"github.com/ipfs/go-graphsync"
	gsimpl "github.com/ipfs/go-graphsync/impl"
	gsnet "github.com/ipfs/go-graphsync/network"
	"github.com/ipfs/go-graphsync/storeutil"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/host"
	"go.opentelemetry.io/otel"
)

var Tracer = otel.Tracer("filclient")

var log = logging.Logger("filclient")
var retrievalLogger = logging.Logger("filclient-retrieval")

const DealProtocolv110 = "/fil/storage/mk/1.1.0"
const DealProtocolv120 = "/fil/storage/mk/1.2.0"
const QueryAskProtocol = "/fil/storage/ask/1.1.0"
const DealStatusProtocolv110 = "/fil/storage/status/1.1.0"
const DealStatusProtocolv120 = "/fil/storage/status/1.2.0"
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

	logRetrievalProgressEvents bool

	Libp2pTransferMgr *libp2pTransferManager

	retrievalEventPublisher *rep.RetrievalEventPublisher
}

type GetPieceCommFunc func(ctx context.Context, payloadCid cid.Cid, bstore blockstore.Blockstore) (cid.Cid, uint64, abi.UnpaddedPieceSize, error)

type Lp2pDTConfig struct {
	Server          httptransport.ServerConfig
	TransferTimeout time.Duration
}

type Config struct {
	DataDir                    string
	GraphsyncOpts              []gsimpl.Option
	Api                        api.Gateway
	Wallet                     *wallet.LocalWallet
	Addr                       address.Address
	Blockstore                 blockstore.Blockstore
	Datastore                  datastore.Batching
	Host                       host.Host
	ChannelMonitorConfig       channelmonitor.Config
	RetrievalConfigurer        datatransfer.TransportConfigurer
	LogRetrievalProgressEvents bool
	Lp2pDTConfig               Lp2pDTConfig
}

func NewClient(
	h host.Host,
	api api.Gateway,
	w *wallet.LocalWallet,
	addr address.Address,
	bs blockstore.Blockstore,
	ds datastore.Batching,
	ddir string,
	opts ...func(*Config),
) (*FilClient, error) {
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
		Lp2pDTConfig: Lp2pDTConfig{
			Server: httptransport.ServerConfig{
				// Keep the cache around for one minute after a request
				// finishes in case the connection bounced in the middle
				// of a transfer, or there is a request for the same payload
				// soon after
				BlockInfoCacheManager: boostcar.NewDelayedUnrefBICM(time.Minute),
				ThrottleLimit:         uint(100),
			},
			// Wait up to 24 hours for the transfer to complete (including
			// after a connection bounce) before erroring out the deal
			TransferTimeout: 24 * time.Hour,
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
	tpt := gst.NewTransport(cfg.Host.ID(), gse)

	dtRestartConfig := dtimpl.ChannelRestartConfig(cfg.ChannelMonitorConfig)

	cidlistsdirPath := filepath.Join(cfg.DataDir, "cidlistsdir")
	if err := os.MkdirAll(cidlistsdirPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to initialize cidlistsdir: %w", err)
	}

	mgr, err := dtimpl.NewDataTransfer(cfg.Datastore, dtn, tpt, dtRestartConfig)
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

	if err := mgr.Start(ctx); err != nil {
		return nil, err
	}

	/*
		mgr.SubscribeToEvents(func(event datatransfer.Event, channelState datatransfer.ChannelState) {
			fmt.Printf("[%s] event: %s %d %s %s\n", time.Now().Format("15:04:05"), event.Message, event.Code, datatransfer.Events[event.Code], event.Timestamp.Format("15:04:05"))
			fmt.Printf("channelstate: %s %s\n", datatransfer.Statuses[channelState.Status()], channelState.Message())
		})
	*/

	// Create a server for libp2p data transfers
	lp2pds := namespace.Wrap(cfg.Datastore, datastore.NewKey("/libp2p-dt"))
	authDB := httptransport.NewAuthTokenDB(lp2pds)
	dtServer := httptransport.NewLibp2pCarServer(cfg.Host, authDB, cfg.Blockstore, cfg.Lp2pDTConfig.Server)

	// Create a manager to watch for libp2p data transfer events
	lp2pXferOpts := libp2pTransferManagerOpts{
		xferTimeout:     cfg.Lp2pDTConfig.TransferTimeout,
		authCheckPeriod: time.Minute,
	}
	libp2pTransferMgr := newLibp2pTransferManager(dtServer, lp2pds, authDB, lp2pXferOpts)
	if err := libp2pTransferMgr.Start(ctx); err != nil {
		return nil, err
	}

	// Create a retrieval event publisher
	retrievalEventPublisher := rep.New(ctx)

	fc := &FilClient{
		host:                       cfg.Host,
		api:                        cfg.Api,
		wallet:                     cfg.Wallet,
		ClientAddr:                 cfg.Addr,
		blockstore:                 cfg.Blockstore,
		dataTransfer:               mgr,
		pchmgr:                     pchmgr,
		mpusher:                    mpusher,
		computePieceComm:           GeneratePieceCommitment,
		graphSync:                  gse,
		transport:                  tpt,
		logRetrievalProgressEvents: cfg.LogRetrievalProgressEvents,
		Libp2pTransferMgr:          libp2pTransferMgr,
		retrievalEventPublisher:    retrievalEventPublisher,
	}

	// Subscribe this FilClient instance to retrieval events
	retrievalEventPublisher.Subscribe(fc)

	return fc, nil
}

func (fc *FilClient) GetDtMgr() datatransfer.Manager {
	return fc.dataTransfer
}

func (fc *FilClient) SetPieceCommFunc(pcf GetPieceCommFunc) {
	fc.computePieceComm = pcf
}

func (fc *FilClient) GraphSyncStats() graphsync.Stats {
	return fc.graphSync.Stats()
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
