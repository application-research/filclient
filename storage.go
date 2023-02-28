package filclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	smtypes "github.com/filecoin-project/boost/storagemarket/types"
	"github.com/filecoin-project/boost/storagemarket/types/dealcheckpoints"
	"github.com/filecoin-project/boost/transport/httptransport"
	boosttypes "github.com/filecoin-project/boost/transport/types"
	"github.com/filecoin-project/go-address"
	cborutil "github.com/filecoin-project/go-cbor-util"
	"github.com/filecoin-project/go-commp-utils/writer"
	datatransfer "github.com/filecoin-project/go-data-transfer"
	gst "github.com/filecoin-project/go-data-transfer/transport/graphsync"
	commcid "github.com/filecoin-project/go-fil-commcid"
	commp "github.com/filecoin-project/go-fil-commp-hashhash"
	"github.com/filecoin-project/go-fil-markets/storagemarket"
	"github.com/filecoin-project/go-fil-markets/storagemarket/impl/clientutils"
	"github.com/filecoin-project/go-fil-markets/storagemarket/impl/requestvalidation"
	"github.com/filecoin-project/go-fil-markets/storagemarket/network"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/go-state-types/builtin"
	"github.com/filecoin-project/go-state-types/builtin/v9/market"
	"github.com/filecoin-project/lotus/api"
	"github.com/filecoin-project/lotus/chain/types"
	"github.com/google/uuid"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-graphsync"
	"github.com/ipfs/go-graphsync/peerstate"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	"github.com/ipld/go-car"
	selectorparse "github.com/ipld/go-ipld-prime/traversal/selector/parse"
	inet "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/multiformats/go-multiaddr"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

func (fc *FilClient) GetAsk(ctx context.Context, maddr address.Address) (*network.AskResponse, error) {
	ctx, span := Tracer.Start(ctx, "doGetAsk", trace.WithAttributes(
		attribute.Stringer("miner", maddr),
	))
	defer span.End()

	s, err := fc.streamToMiner(ctx, maddr, QueryAskProtocol)
	if err != nil {
		return nil, err
	}
	fc.host.ConnManager().Protect(s.Conn().RemotePeer(), "GetAsk")
	defer func() {
		fc.host.ConnManager().Unprotect(s.Conn().RemotePeer(), "GetAsk")
		s.Close()
	}()

	areq := &network.AskRequest{Miner: maddr}
	var resp network.AskResponse

	// Sending the query ask and reading the response
	if err := doRpc(ctx, s, areq, &resp); err != nil {
		return nil, fmt.Errorf("get ask rpc: %w", err)
	}

	return &resp, nil
}

const epochsPerHour = 60 * 2

func ComputePrice(askPrice types.BigInt, size abi.PaddedPieceSize, duration abi.ChainEpoch) (*abi.TokenAmount, error) {
	cost := big.Mul(big.Div(big.Mul(big.NewInt(int64(size)), askPrice), big.NewInt(1<<30)), big.NewInt(int64(duration)))

	return (*abi.TokenAmount)(&cost), nil
}

func (fc *FilClient) DealProtocolForMiner(ctx context.Context, miner address.Address) (protocol.ID, error) {
	// Connect to the miner. If there's not already a connection to the miner,
	// libp2p will open a connection and exchange protocol IDs.
	mpid, err := fc.ConnectToMiner(ctx, miner)
	if err != nil {
		return "", fmt.Errorf("connecting to %s: %w", miner, err)
	}

	// Get the supported deal protocols for the miner's peer
	proto, err := fc.host.Peerstore().FirstSupportedProtocol(mpid, DealProtocolv120, DealProtocolv110)
	if err != nil {
		return "", fmt.Errorf("getting deal protocol for %s: %w", miner, err)
	}
	if proto == "" {
		return "", fmt.Errorf("%s does not support any deal making protocol", miner)
	}

	return protocol.ID(proto), nil
}

type DealPieceInfo struct {
	// Piece CID
	Cid cid.Cid

	// Piece size
	Size abi.PaddedPieceSize

	// Payload size
	PayloadSize uint64
}

type DealConfig struct {
	Verified      bool
	FastRetrieval bool
	MinSize       abi.PaddedPieceSize
	PieceInfo     DealPieceInfo
}

func DefaultDealConfig() DealConfig {
	return DealConfig{
		Verified:      false,
		FastRetrieval: false,
		MinSize:       0,
		PieceInfo:     DealPieceInfo{},
	}
}

type DealOption func(*DealConfig)

// Whether to use verified FIL.
func DealWithVerified(verified bool) DealOption {
	return func(cfg *DealConfig) {
		cfg.Verified = verified
	}
}

// Whether to request for the provider to keep an unsealed copy.
func DealWithFastRetrieval(fastRetrieval bool) DealOption {
	return func(cfg *DealConfig) {
		cfg.FastRetrieval = fastRetrieval
	}
}

// If the computed piece size is smaller than minSize, minSize will be used
// instead.
func DealWithMinSize(minSize abi.PaddedPieceSize) DealOption {
	return func(cfg *DealConfig) {
		cfg.MinSize = minSize
	}
}

// This can be used to pass a precomputed piece cid and size. If it's not
// passed, the piece commitment will be automatically calculated.
func DealWithPieceInfo(pieceInfo DealPieceInfo) DealOption {
	return func(cfg *DealConfig) {
		cfg.PieceInfo = pieceInfo
	}
}

func DealWithConfig(newCfg DealConfig) DealOption {
	return func(cfg *DealConfig) {
		*cfg = newCfg
	}
}

func (fc *FilClient) MakeDealWithOptions(
	ctx context.Context,
	sp address.Address,
	payload cid.Cid,
	price types.BigInt,
	duration abi.ChainEpoch,
	options ...DealOption,
) (*network.Proposal, error) {
	ctx, span := Tracer.Start(ctx, "makeDeal", trace.WithAttributes(
		attribute.Stringer("sp", sp),
		attribute.Stringer("price", price),
		attribute.Int64("duration", int64(duration)),
		attribute.Stringer("cid", payload),
	))
	defer span.End()

	cfg := DefaultDealConfig()
	for _, option := range options {
		option(&cfg)
	}

	// If no piece CID was provided, calculate it now
	if cfg.PieceInfo.Cid == cid.Undef {
		pieceCid, payloadSize, unpaddedPieceSize, err := fc.computePieceComm(ctx, payload, fc.blockstore)
		if err != nil {
			return nil, err
		}

		cfg.PieceInfo = DealPieceInfo{
			Cid:         pieceCid,
			Size:        unpaddedPieceSize.Padded(),
			PayloadSize: payloadSize,
		}
	}

	if cfg.PieceInfo.Size < cfg.MinSize {
		paddedPieceCid, err := ZeroPadPieceCommitment(cfg.PieceInfo.Cid, cfg.PieceInfo.Size.Unpadded(), cfg.MinSize.Unpadded())
		if err != nil {
			return nil, err
		}

		cfg.PieceInfo.Cid = paddedPieceCid
		cfg.PieceInfo.Size = cfg.MinSize
	}

	// After this point, cfg.pieceInfo can be considered VALID

	head, err := fc.api.ChainHead(ctx)
	if err != nil {
		return nil, err
	}

	collBounds, err := fc.api.StateDealProviderCollateralBounds(ctx, cfg.PieceInfo.Size, cfg.Verified, types.EmptyTSK)
	if err != nil {
		return nil, err
	}

	// set provider collateral 10% above minimum to avoid fluctuations causing deal failure
	provCol := big.Div(big.Mul(collBounds.Min, big.NewInt(11)), big.NewInt(10))

	// give miners a week to seal and commit the sector
	dealStart := head.Height() + (epochsPerHour * 24 * 7)

	end := dealStart + duration

	pricePerEpoch := big.Div(big.Mul(big.NewInt(int64(cfg.PieceInfo.Size)), price), big.NewInt(1<<30))

	label, err := clientutils.LabelField(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to construct label field: %w", err)
	}

	proposal := &market.DealProposal{
		PieceCID:     cfg.PieceInfo.Cid,
		PieceSize:    cfg.PieceInfo.Size,
		VerifiedDeal: cfg.Verified,
		Client:       fc.ClientAddr,
		Provider:     sp,

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
			Root:         payload,
			RawBlockSize: cfg.PieceInfo.PayloadSize,
		},
		FastRetrieval: cfg.FastRetrieval,
	}, nil
}

// Deprecated: MakeDeal will be removed and MakeDealWithOptions will be renamed
// to MakeDeal.
func (fc *FilClient) MakeDeal(
	ctx context.Context,
	miner address.Address,
	data cid.Cid,
	price types.BigInt,
	minSize abi.PaddedPieceSize,
	duration abi.ChainEpoch,
	verified bool,
	removeUnsealed bool,
) (*network.Proposal, error) {
	return fc.MakeDealWithOptions(
		ctx,
		miner,
		data,
		price,
		duration,
		DealWithMinSize(minSize),
		DealWithVerified(verified),
		DealWithFastRetrieval(!removeUnsealed),
	)
}

func (fc *FilClient) SendProposalV110(ctx context.Context, netprop network.Proposal, propCid cid.Cid) (bool, error) {
	ctx, span := Tracer.Start(ctx, "sendProposalV110")
	defer span.End()

	s, err := fc.streamToMiner(ctx, netprop.DealProposal.Proposal.Provider, DealProtocolv110)
	if err != nil {
		return false, fmt.Errorf("opening stream to miner: %w", err)
	}

	fc.host.ConnManager().Protect(s.Conn().RemotePeer(), "SendProposalV110")
	defer func() {
		fc.host.ConnManager().Unprotect(s.Conn().RemotePeer(), "SendProposalV110")
		s.Close()
	}()

	// Send proposal to provider using deal protocol v1.1.0 format
	var resp network.SignedResponse
	if err := doRpc(ctx, s, &netprop, &resp); err != nil {
		return false, fmt.Errorf("send proposal rpc: %w", err)
	}

	switch resp.Response.State {
	case storagemarket.StorageDealError:
		return true, fmt.Errorf("error response from miner: %s", resp.Response.Message)
	case storagemarket.StorageDealProposalRejected:
		return true, fmt.Errorf("deal rejected by miner: %s", resp.Response.Message)
	case storagemarket.StorageDealWaitingForData, storagemarket.StorageDealProposalAccepted:
	default:
		return true, fmt.Errorf("unrecognized response from miner: %d %s", resp.Response.State, resp.Response.Message)
	}

	if propCid != resp.Response.Proposal {
		return true, fmt.Errorf("proposal in saved deal did not match response (%s != %s)", propCid, resp.Response.Proposal)
	}

	return false, nil
}

type ProposalV120Config struct {
	DealUUID         uuid.UUID
	Transfer         smtypes.Transfer
	Offline          bool
	SkipIPNIAnnounce bool
}

func DefaultProposalV120Config() ProposalV120Config {
	return ProposalV120Config{
		DealUUID: uuid.New(),
		Transfer: smtypes.Transfer{},
	}
}

type ProposalV120Option func(*ProposalV120Config, network.Proposal) error

func ProposalV120WithDealUUID(dealUUID uuid.UUID) ProposalV120Option {
	return func(cfg *ProposalV120Config, netprop network.Proposal) error {
		cfg.DealUUID = dealUUID
		return nil
	}
}

func ProposalV120WithLibp2pTransfer(
	announceAddr multiaddr.Multiaddr,
	authToken string,
	clientID uint,
) ProposalV120Option {
	return func(cfg *ProposalV120Config, netprop network.Proposal) error {
		transferParams, err := json.Marshal(boosttypes.HttpRequest{
			URL: "libp2p://" + announceAddr.String(),
			Headers: map[string]string{
				"Authorization": httptransport.BasicAuthHeader("", authToken),
			},
		})
		if err != nil {
			return fmt.Errorf("failed to marshal libp2p transfer params: %v", err)
		}

		cfg.Transfer = smtypes.Transfer{
			Type:     "libp2p",
			ClientID: fmt.Sprintf("%d", clientID),
			Params:   transferParams,
			Size:     netprop.Piece.RawBlockSize,
		}

		return nil
	}
}

func ProposalV120WithTransfer(transfer smtypes.Transfer) ProposalV120Option {
	return func(cfg *ProposalV120Config, netprop network.Proposal) error {
		cfg.Transfer = transfer
		return nil
	}
}

func ProposalV120WithOffline(offline bool) ProposalV120Option {
	return func(cfg *ProposalV120Config, netprop network.Proposal) error {
		cfg.Offline = offline
		return nil
	}
}

func ProposalV120WithSkipIPNIAnnounce(skip bool) ProposalV120Option {
	return func(cfg *ProposalV120Config, netprop network.Proposal) error {
		cfg.SkipIPNIAnnounce = skip
		return nil
	}
}

func ProposalV120WithConfig(newCfg ProposalV120Config) ProposalV120Option {
	return func(cfg *ProposalV120Config, netprop network.Proposal) error {
		*cfg = newCfg
		return nil
	}
}

// Deprecated: SendProposalV120 will be removed and SendProposalV120WithOptions
// will be replaced by SendProposalV120
func (fc *FilClient) SendProposalV120(
	ctx context.Context,
	dbid uint,
	netprop network.Proposal,
	dealUUID uuid.UUID,
	announce multiaddr.Multiaddr,
	authToken string,
) (bool, error) {
	return fc.SendProposalV120WithOptions(
		ctx,
		netprop,
		ProposalV120WithDealUUID(dealUUID),
		ProposalV120WithLibp2pTransfer(announce, authToken, dbid),
	)
}

func (fc *FilClient) SendProposalV120WithOptions(ctx context.Context, netprop network.Proposal, options ...ProposalV120Option) (bool, error) {
	ctx, span := Tracer.Start(ctx, "sendProposalV120")
	defer span.End()

	// Gen config with options
	cfg := DefaultProposalV120Config()
	for _, option := range options {
		if err := option(&cfg, netprop); err != nil {
			return false, fmt.Errorf("failed to apply option %T: %v", option, err)
		}
	}

	s, err := fc.streamToMiner(ctx, netprop.DealProposal.Proposal.Provider, DealProtocolv120)
	if err != nil {
		return false, fmt.Errorf("opening stream to miner: %w", err)
	}

	fc.host.ConnManager().Protect(s.Conn().RemotePeer(), "SendProposalV120")
	defer func() {
		fc.host.ConnManager().Unprotect(s.Conn().RemotePeer(), "SendProposalV120")
		s.Close()
	}()

	// Send proposal to storage provider using deal protocol v1.2.0 format
	params := smtypes.DealParams{
		DealUUID:           cfg.DealUUID,
		ClientDealProposal: *netprop.DealProposal,
		DealDataRoot:       netprop.Piece.Root,
		Transfer:           cfg.Transfer,
		RemoveUnsealedCopy: !netprop.FastRetrieval,
		IsOffline:          cfg.Offline,
		SkipIPNIAnnounce:   cfg.SkipIPNIAnnounce,
	}

	var resp smtypes.DealResponse
	if err := doRpc(ctx, s, &params, &resp); err != nil {
		return false, fmt.Errorf("send proposal rpc: %w", err)
	}

	// Check if the deal proposal was accepted
	if !resp.Accepted {
		return true, fmt.Errorf("deal proposal rejected: %s", resp.Message)
	}

	return false, nil
}

func GeneratePieceCommitment(ctx context.Context, payloadCid cid.Cid, bstore blockstore.Blockstore) (cid.Cid, uint64, abi.UnpaddedPieceSize, error) {
	selectiveCar := car.NewSelectiveCar(
		context.Background(),
		bstore,
		[]car.Dag{{Root: payloadCid, Selector: selectorparse.CommonSelector_ExploreAllRecursively}},
		car.MaxTraversalLinks(maxTraversalLinks),
		car.TraverseLinksOnlyOnce(),
	)
	preparedCar, err := selectiveCar.Prepare()
	if err != nil {
		return cid.Undef, 0, 0, err
	}

	writer := new(commp.Calc)
	err = preparedCar.Dump(ctx, writer)
	if err != nil {
		return cid.Undef, 0, 0, err
	}

	commpc, size, err := writer.Digest()
	if err != nil {
		return cid.Undef, 0, 0, err
	}

	commCid, err := commcid.DataCommitmentV1ToCID(commpc)
	if err != nil {
		return cid.Undef, 0, 0, err
	}

	return commCid, preparedCar.Size(), abi.PaddedPieceSize(size).Unpadded(), nil
}

func GeneratePieceCommitmentFFI(ctx context.Context, payloadCid cid.Cid, bstore blockstore.Blockstore) (cid.Cid, uint64, abi.UnpaddedPieceSize, error) {
	selectiveCar := car.NewSelectiveCar(
		context.Background(),
		bstore,
		[]car.Dag{{Root: payloadCid, Selector: selectorparse.CommonSelector_ExploreAllRecursively}},
		car.MaxTraversalLinks(maxTraversalLinks),
		car.TraverseLinksOnlyOnce(),
	)
	preparedCar, err := selectiveCar.Prepare()
	if err != nil {
		return cid.Undef, 0, 0, err
	}

	commpWriter := &writer.Writer{}
	err = preparedCar.Dump(ctx, commpWriter)
	if err != nil {
		return cid.Undef, 0, 0, err
	}

	dataCIDSize, err := commpWriter.Sum()
	if err != nil {
		return cid.Undef, 0, 0, err
	}

	return dataCIDSize.PieceCID, preparedCar.Size(), dataCIDSize.PieceSize.Unpadded(), nil
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

func (fc *FilClient) DealStatus(ctx context.Context, miner address.Address, propCid cid.Cid, dealUUID *uuid.UUID) (*storagemarket.ProviderDealState, error) {
	protos := []protocol.ID{DealStatusProtocolv110}
	if dealUUID != nil {
		// Deal status protocol v1.2.0 requires a deal uuid, so only include it
		// if the deal uuid is not nil
		protos = []protocol.ID{DealStatusProtocolv120, DealStatusProtocolv110}
	}
	s, err := fc.streamToMiner(ctx, miner, protos...)
	if err != nil {
		return nil, err
	}

	fc.host.ConnManager().Protect(s.Conn().RemotePeer(), "DealStatus")
	defer func() {
		fc.host.ConnManager().Unprotect(s.Conn().RemotePeer(), "DealStatus")
		s.Close()
	}()

	// If the miner only supports deal status protocol v1.1.0,
	// or we don't have a uuid for the deal
	if s.Protocol() == DealStatusProtocolv110 {
		// Query deal status by signed proposal cid
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

		var resp network.DealStatusResponse
		if err := doRpc(ctx, s, req, &resp); err != nil {
			return nil, fmt.Errorf("deal status rpc: %w", err)
		}

		return &resp.DealState, nil
	}

	// The miner supports deal status protocol v1.2.0 or above.
	// Query deal status by deal UUID.
	uuidBytes, err := dealUUID.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("getting uuid bytes: %w", err)
	}
	sig, err := fc.wallet.WalletSign(ctx, fc.ClientAddr, uuidBytes, api.MsgMeta{Type: api.MTUnknown})
	if err != nil {
		return nil, fmt.Errorf("signing status request failed: %w", err)
	}

	req := &smtypes.DealStatusRequest{
		DealUUID:  *dealUUID,
		Signature: *sig,
	}

	var resp smtypes.DealStatusResponse
	if err := doRpc(ctx, s, req, &resp); err != nil {
		return nil, fmt.Errorf("deal status rpc: %w", err)
	}

	if resp.Error != "" {
		return nil, fmt.Errorf("deal status error: %s", resp.Error)
	}

	st := resp.DealStatus
	if st == nil {
		return nil, fmt.Errorf("deal status is nil")
	}

	return &storagemarket.ProviderDealState{
		State:         toLegacyDealStatus(st),
		Message:       st.Error,
		Proposal:      &st.Proposal,
		ProposalCid:   &st.SignedProposalCid,
		PublishCid:    st.PublishCid,
		DealID:        st.ChainDealID,
		FastRetrieval: true,
	}, nil
}

// toLegacyDealStatus converts a v1.2.0 deal status to a legacy deal status
func toLegacyDealStatus(ds *smtypes.DealStatus) storagemarket.StorageDealStatus {
	if ds.Error != "" {
		return storagemarket.StorageDealError
	}

	switch ds.Status {
	case dealcheckpoints.Accepted.String():
		return storagemarket.StorageDealWaitingForData
	case dealcheckpoints.Transferred.String():
		return storagemarket.StorageDealVerifyData
	case dealcheckpoints.Published.String():
		return storagemarket.StorageDealPublishing
	case dealcheckpoints.PublishConfirmed.String():
		return storagemarket.StorageDealStaged
	case dealcheckpoints.AddedPiece.String():
		return storagemarket.StorageDealAwaitingPreCommit
	case dealcheckpoints.IndexedAndAnnounced.String():
		return storagemarket.StorageDealAwaitingPreCommit
	case dealcheckpoints.Complete.String():
		return storagemarket.StorageDealSealing
	}

	return storagemarket.StorageDealUnknown
}

type TransferType string

const (
	BoostTransfer     TransferType = "boost"
	GraphsyncTransfer TransferType = "graphsync"
)

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

	TransferID string `json:"transferId"`

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

	Stages *datatransfer.ChannelStages

	TransferType TransferType
}

func ChannelStateConv(st datatransfer.ChannelState) *ChannelState {
	return &ChannelState{
		SelfPeer:     st.SelfPeer(),
		RemotePeer:   st.OtherPeer(),
		Status:       st.Status(),
		StatusStr:    datatransfer.Statuses[st.Status()],
		Sent:         st.Sent(),
		Received:     st.Received(),
		Message:      st.Message(),
		BaseCid:      st.BaseCID().String(),
		ChannelID:    st.ChannelID(),
		TransferID:   st.ChannelID().String(),
		Stages:       st.Stages(),
		TransferType: GraphsyncTransfer,
		//Vouchers:          st.Vouchers(),
		//VoucherResults:    st.VoucherResults(),
		//LastVoucher:       st.LastVoucher(),
		//LastVoucherResult: st.LastVoucherResult(),
		//ReceivedCids:      st.ReceivedCids(),
		//Queued:            st.Queued(),
	}
}

func (fc *FilClient) V110TransfersInProgress(ctx context.Context) (map[datatransfer.ChannelID]datatransfer.ChannelState, error) {
	return fc.dataTransfer.InProgressChannels(ctx)
}

func (fc *FilClient) TransfersInProgress(ctx context.Context) (map[string]*ChannelState, error) {
	v1dts, err := fc.dataTransfer.InProgressChannels(ctx)
	if err != nil {
		return nil, err
	}

	v2dts, err := fc.Libp2pTransferMgr.All()
	if err != nil {
		return nil, err
	}

	dts := make(map[string]*ChannelState, len(v1dts)+len(v2dts))
	for id, dt := range v1dts {
		dts[id.String()] = ChannelStateConv(dt)
	}
	for id, dt := range v2dts {
		dtcp := dt
		dts[id] = &dtcp
	}

	return dts, nil
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
	mpid, err := fc.MinerPeer(ctx, miner)
	if err != nil {
		return nil, err
	}
	// gather information about active transport channels
	transportChannels := fc.transport.ChannelsForPeer(mpid.ID)
	// gather information about graphsync state for peer
	gsPeerState := fc.graphSync.PeerState(mpid.ID)

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
		diagnostics = append(diagnostics, fmt.Sprintf("No data transfer channel id for GraphSync request ID %s", requestID))
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

func (fc *FilClient) TransferStatus(ctx context.Context, chanid *datatransfer.ChannelID) (*ChannelState, error) {
	st, err := fc.dataTransfer.ChannelState(ctx, *chanid)
	if err != nil {
		return nil, err
	}

	return ChannelStateConv(st), nil
}

func (fc *FilClient) TransferStatusByID(ctx context.Context, id string) (*ChannelState, error) {
	chid, err := ChannelIDFromString(id)
	if err == nil {
		// If the id is a data transfer channel id, get the transfer status by channel id
		return fc.TransferStatus(ctx, chid)
	}

	// Get the transfer status by transfer id
	return fc.Libp2pTransferMgr.byId(id)
}

var ErrNoTransferFound = fmt.Errorf("no transfer found")

func (fc *FilClient) TransferStatusForContent(ctx context.Context, content cid.Cid, miner address.Address) (*ChannelState, error) {
	start := time.Now()
	defer func() {
		log.Infof("check transfer status took: %s", time.Since(start))
	}()
	mpid, err := fc.MinerPeer(ctx, miner)
	if err != nil {
		return nil, err
	}

	// Check if there's a storage deal transfer with the miner that matches the
	// payload CID
	// 1. over data transfer v1.2
	xfer, err := fc.Libp2pTransferMgr.byRemoteAddrAndPayloadCid(mpid.ID.Pretty(), content)
	if err != nil {
		return nil, err
	}
	if xfer != nil {
		return xfer, nil
	}

	// 2. over data transfer v1.1
	inprog, err := fc.dataTransfer.InProgressChannels(ctx)
	if err != nil {
		return nil, err
	}

	for chanid, state := range inprog {
		if chanid.Responder == mpid.ID {
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

	mpid, err := fc.ConnectToMiner(ctx, miner)
	if err != nil {
		return nil, err
	}

	voucher := &requestvalidation.StorageDataTransferVoucher{Proposal: propCid}

	fc.host.ConnManager().Protect(mpid, "transferring")

	chanid, err := fc.dataTransfer.OpenPushDataChannel(ctx, mpid, voucher, dataCid, selectorparse.CommonSelector_ExploreAllRecursively)
	if err != nil {
		return nil, fmt.Errorf("opening push data channel: %w", err)
	}

	return &chanid, nil
}

func (fc *FilClient) SubscribeToDataTransferEvents(f datatransfer.Subscriber) func() {
	return fc.dataTransfer.SubscribeToEvents(f)
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
		mpid, err := fc.ConnectToMiner(ctx, miner)
		if err != nil {
			return err
		}

		if mpid != st.RemotePeer {
			return fmt.Errorf("miner peer ID is different than RemotePeer in data transfer channel")
		}
	}

	return fc.dataTransfer.RestartDataTransferChannel(ctx, st.ChannelID)

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
