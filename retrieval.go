package filclient

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/application-research/filclient/rep"
	"github.com/filecoin-project/go-address"
	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/filecoin-project/go-fil-markets/retrievalmarket"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/go-state-types/builtin/v8/paych"
	"github.com/filecoin-project/lotus/chain/types"
	"github.com/filecoin-project/lotus/paychmgr"
	"github.com/ipfs/go-cid"
	selectorparse "github.com/ipld/go-ipld-prime/traversal/selector/parse"
	"github.com/libp2p/go-libp2p/core/peer"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

func (fc *FilClient) RetrievalQuery(ctx context.Context, maddr address.Address, pcid cid.Cid) (*retrievalmarket.QueryResponse, error) {
	ctx, span := Tracer.Start(ctx, "retrievalQuery", trace.WithAttributes(
		attribute.Stringer("miner", maddr),
	))
	defer span.End()

	s, err := fc.streamToMiner(ctx, maddr, RetrievalQueryProtocol)
	if err != nil {
		// publish fail event, log the err
		fc.retrievalEventPublisher.Publish(
			rep.NewRetrievalEventFailure(rep.QueryPhase, pcid, "", maddr,
				fmt.Sprintf("failed connecting to miner: %s", err.Error())))
		return nil, err
	}

	fc.host.ConnManager().Protect(s.Conn().RemotePeer(), "RetrievalQuery")
	defer func() {
		fc.host.ConnManager().Unprotect(s.Conn().RemotePeer(), "RetrievalQuery")
		s.Close()
	}()

	// We have connected
	// publish connected event
	fc.retrievalEventPublisher.Publish(rep.NewRetrievalEventConnect(rep.QueryPhase, pcid, "", maddr))

	q := &retrievalmarket.Query{
		PayloadCID: pcid,
	}

	var resp retrievalmarket.QueryResponse
	if err := doRpc(ctx, s, q, &resp); err != nil {
		// publish failure event
		fc.retrievalEventPublisher.Publish(
			rep.NewRetrievalEventFailure(rep.QueryPhase, pcid, "", maddr,
				fmt.Sprintf("failed retrieval query ask: %s", err.Error())))
		return nil, fmt.Errorf("retrieval query rpc: %w", err)
	}

	// publish query ask event
	fc.retrievalEventPublisher.Publish(rep.NewRetrievalEventQueryAsk(rep.QueryPhase, pcid, "", maddr, resp))

	return &resp, nil
}

func (fc *FilClient) RetrievalQueryToPeer(ctx context.Context, minerPeer peer.AddrInfo, pcid cid.Cid) (*retrievalmarket.QueryResponse, error) {
	ctx, span := Tracer.Start(ctx, "retrievalQueryPeer", trace.WithAttributes(
		attribute.Stringer("peerID", minerPeer.ID),
	))
	defer span.End()

	s, err := fc.openStreamToPeer(ctx, minerPeer, RetrievalQueryProtocol)
	if err != nil {
		// publish fail event, log the err
		fc.retrievalEventPublisher.Publish(
			rep.NewRetrievalEventFailure(rep.QueryPhase, pcid, minerPeer.ID, address.Undef,
				fmt.Sprintf("failed connecting to miner: %s", err.Error())))
		return nil, err
	}

	fc.host.ConnManager().Protect(s.Conn().RemotePeer(), "RetrievalQueryToPeer")
	defer func() {
		fc.host.ConnManager().Unprotect(s.Conn().RemotePeer(), "RetrievalQueryToPeer")
		s.Close()
	}()

	// We have connected
	// publish connected event
	fc.retrievalEventPublisher.Publish(rep.NewRetrievalEventConnect(rep.QueryPhase, pcid, minerPeer.ID, address.Address{}))

	q := &retrievalmarket.Query{
		PayloadCID: pcid,
	}

	var resp retrievalmarket.QueryResponse
	if err := doRpc(ctx, s, q, &resp); err != nil {
		// publish failure event
		fc.retrievalEventPublisher.Publish(
			rep.NewRetrievalEventFailure(rep.QueryPhase, pcid, minerPeer.ID, address.Undef,
				fmt.Sprintf("failed retrieval query ask: %s", err.Error())))
		return nil, fmt.Errorf("retrieval query rpc: %w", err)
	}

	// publish query ask event
	fc.retrievalEventPublisher.Publish(rep.NewRetrievalEventQueryAsk(rep.QueryPhase, pcid, minerPeer.ID, address.Undef, resp))

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

	amount := types.BigMul(types.BigInt(reqBalance), types.NewInt(2))

	fmt.Println("getting payment channel: ", fc.ClientAddr, dest, amount)
	pchaddr, mcid, err := fc.pchmgr.GetPaych(ctx, fc.ClientAddr, dest, amount, paychmgr.GetOpts{
		Reserve:  false,
		OffChain: false,
	})
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

	log.Infof("Starting retrieval with miner: %s", miner)

	minerPeer, err := fc.MinerPeer(ctx, miner)
	if err != nil {
		return nil, err
	}
	minerOwner, err := fc.minerOwner(ctx, miner)
	if err != nil {
		return nil, err
	}
	return fc.RetrieveContentFromPeerWithProgressCallback(ctx, minerPeer.ID, minerOwner, proposal, progressCallback)
}

func (fc *FilClient) RetrieveContentFromPeerWithProgressCallback(
	ctx context.Context,
	peerID peer.ID,
	minerWallet address.Address,
	proposal *retrievalmarket.DealProposal,
	progressCallback func(bytesReceived uint64),
) (*RetrievalStats, error) {
	return fc.retrieveContentFromPeerWithProgressCallback(ctx, peerID, minerWallet, proposal, progressCallback, nil)
}

type RetrievalResult struct {
	*RetrievalStats
	Err error
}

func (fc *FilClient) RetrieveContentFromPeerAsync(
	ctx context.Context,
	peerID peer.ID,
	minerWallet address.Address,
	proposal *retrievalmarket.DealProposal,
) (result <-chan RetrievalResult, onProgress <-chan uint64, gracefulShutdown func()) {
	gracefulShutdownChan := make(chan struct{}, 1)
	resultChan := make(chan RetrievalResult, 1)
	progressChan := make(chan uint64)
	internalCtx, internalCancel := context.WithCancel(ctx)
	go func() {
		defer internalCancel()
		result, err := fc.retrieveContentFromPeerWithProgressCallback(internalCtx, peerID, minerWallet, proposal, func(bytes uint64) {
			select {
			case <-internalCtx.Done():
			case progressChan <- bytes:
			}
		}, gracefulShutdownChan)
		resultChan <- RetrievalResult{result, err}
	}()
	return resultChan, progressChan, func() {
		gracefulShutdownChan <- struct{}{}
	}
}

func (fc *FilClient) retrieveContentFromPeerWithProgressCallback(
	ctx context.Context,
	peerID peer.ID,
	minerWallet address.Address,
	proposal *retrievalmarket.DealProposal,
	progressCallback func(bytesReceived uint64),
	gracefulShutdownRequested <-chan struct{},
) (*RetrievalStats, error) {
	if progressCallback == nil {
		progressCallback = func(bytesReceived uint64) {}
	}

	log.Infof("Starting retrieval with miner peer ID: %s", peerID)

	ctx, span := Tracer.Start(ctx, "fcRetrieveContent")
	defer span.End()

	// Stats
	startTime := time.Now()
	totalPayment := abi.NewTokenAmount(0)

	rootCid := proposal.PayloadCID
	var chanid datatransfer.ChannelID
	var chanidLk sync.Mutex

	pchRequired := !proposal.PricePerByte.IsZero() || !proposal.UnsealPrice.IsZero()
	var pchAddr address.Address
	var pchLane uint64
	if pchRequired {
		// Get the payment channel and create a lane for this retrieval
		pchAddr, err := fc.getPaychWithMinFunds(ctx, minerWallet)
		if err != nil {
			fc.retrievalEventPublisher.Publish(
				rep.NewRetrievalEventFailure(rep.RetrievalPhase, rootCid, peerID, address.Undef,
					fmt.Sprintf("failed to get payment channel: %s", err.Error())))
			return nil, fmt.Errorf("failed to get payment channel: %w", err)
		}
		pchLane, err = fc.pchmgr.AllocateLane(ctx, pchAddr)
		if err != nil {
			fc.retrievalEventPublisher.Publish(
				rep.NewRetrievalEventFailure(rep.RetrievalPhase, rootCid, peerID, address.Undef,
					fmt.Sprintf("failed to allocate lane: %s", err.Error())))
			return nil, fmt.Errorf("failed to allocate lane: %w", err)
		}
	}

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

	dealID := proposal.ID
	allBytesReceived := false
	dealComplete := false
	receivedFirstByte := false

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
			finish(fmt.Errorf("datatransfer error: %s", event.Message))
			return
		case datatransfer.CleanupComplete:
			finish(nil)
			return
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

					// publish deal accepted event
					fc.retrievalEventPublisher.Publish(rep.NewRetrievalEventAccepted(rep.RetrievalPhase, rootCid, peerID, address.Undef))

				// Respond with a payment voucher when funds are requested
				case retrievalmarket.DealStatusFundsNeeded, retrievalmarket.DealStatusFundsNeededLastPayment:
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
							return
						}

						if types.BigCmp(vres.Shortfall, big.NewInt(0)) > 0 {
							finish(fmt.Errorf("not enough funds remaining in payment channel (shortfall = %s)", vres.Shortfall))
							return
						}

						if err := fc.dataTransfer.SendVoucher(ctx, chanidCopy, &retrievalmarket.DealPayment{
							ID:             proposal.ID,
							PaymentChannel: pchAddr,
							PaymentVoucher: vres.Voucher,
						}); err != nil {
							finish(fmt.Errorf("failed to send payment voucher: %w", err))
							return
						}

						nonce++
					} else {
						finish(fmt.Errorf("the miner requested payment even though this transaction was determined to be zero cost"))
						return
					}
				case retrievalmarket.DealStatusRejected:
					finish(fmt.Errorf("deal rejected: %s", resType.Message))
					return
				case retrievalmarket.DealStatusFundsNeededUnseal, retrievalmarket.DealStatusUnsealing:
					finish(fmt.Errorf("data is sealed"))
					return
				case retrievalmarket.DealStatusCancelled:
					finish(fmt.Errorf("deal cancelled: %s", resType.Message))
					return
				case retrievalmarket.DealStatusErrored:
					finish(fmt.Errorf("deal errored: %s", resType.Message))
					return
				case retrievalmarket.DealStatusCompleted:
					if allBytesReceived {
						finish(nil)
						return
					}
					dealComplete = true
				}
			}
		case datatransfer.PauseInitiator:
		case datatransfer.ResumeInitiator:
		case datatransfer.PauseResponder:
		case datatransfer.ResumeResponder:
		case datatransfer.FinishTransfer:
			if dealComplete {
				finish(nil)
				return
			}
			allBytesReceived = true
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
			// First byte has been received

			// publish first byte event
			if !receivedFirstByte {
				receivedFirstByte = true
				fc.retrievalEventPublisher.Publish(rep.NewRetrievalEventFirstByte(rep.RetrievalPhase, rootCid, peerID, address.Undef))
			}

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

		name := datatransfer.Events[event.Code]
		code := event.Code
		msg := event.Message
		blocksIndex := state.ReceivedCidsTotal()
		totalReceived := state.Received()
		if eventCodeNotHandled {
			log.Warnw("unhandled retrieval event", "dealID", dealID, "rootCid", rootCid, "peerID", peerID, "name", name, "code", code, "message", msg, "blocksIndex", blocksIndex, "totalReceived", totalReceived)
		} else {
			if !silenceEventCode || fc.logRetrievalProgressEvents {
				log.Debugw("retrieval event", "dealID", dealID, "rootCid", rootCid, "peerID", peerID, "name", name, "code", code, "message", msg, "blocksIndex", blocksIndex, "totalReceived", totalReceived)
			}
		}
	})
	defer unsubscribe()

	// Submit the retrieval deal proposal to the miner
	newchid, err := fc.dataTransfer.OpenPullDataChannel(ctx, peerID, proposal, proposal.PayloadCID, selectorparse.CommonSelector_ExploreAllRecursively)
	if err != nil {
		// We could fail before a successful proposal
		// publish event failure
		fc.retrievalEventPublisher.Publish(
			rep.NewRetrievalEventFailure(rep.RetrievalPhase, rootCid, peerID, address.Undef,
				fmt.Sprintf("deal proposal failed: %s", err.Error())))
		return nil, err
	}

	// Deal has been proposed
	// publish deal proposed event
	fc.retrievalEventPublisher.Publish(rep.NewRetrievalEventProposed(rep.RetrievalPhase, rootCid, peerID, address.Undef))

	chanidLk.Lock()
	chanid = newchid
	chanidLk.Unlock()

	defer fc.dataTransfer.CloseDataTransferChannel(ctx, chanid)

	// Wait for the retrieval to finish before exiting the function
awaitfinished:
	for {
		select {
		case err := <-dtRes:
			if err != nil {
				// If there is an error, publish a retrieval event failure
				fc.retrievalEventPublisher.Publish(
					rep.NewRetrievalEventFailure(rep.RetrievalPhase, rootCid, peerID, address.Undef,
						fmt.Sprintf("data transfer failed: %s", err.Error())))
				return nil, fmt.Errorf("data transfer failed: %w", err)
			}

			log.Debugf("data transfer for retrieval complete")
			break awaitfinished
		case <-gracefulShutdownRequested:
			go func() {
				fc.dataTransfer.CloseDataTransferChannel(ctx, chanid)
			}()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	// Confirm that we actually ended up with the root block we wanted, failure
	// here indicates a data transfer error that was not properly reported
	if has, err := fc.blockstore.Has(ctx, rootCid); err != nil {
		err = fmt.Errorf("could not get query blockstore: %w", err)
		fc.retrievalEventPublisher.Publish(
			rep.NewRetrievalEventFailure(rep.RetrievalPhase, rootCid, peerID, address.Undef, err.Error()))
		return nil, err
	} else if !has {
		msg := "data transfer failed: unconfirmed block transfer"
		fc.retrievalEventPublisher.Publish(
			rep.NewRetrievalEventFailure(rep.RetrievalPhase, rootCid, peerID, address.Undef, msg))
		return nil, errors.New(msg)
	}

	// Compile the retrieval stats

	state, err := fc.dataTransfer.ChannelState(ctx, chanid)
	if err != nil {
		err = fmt.Errorf("could not get channel state: %w", err)
		fc.retrievalEventPublisher.Publish(
			rep.NewRetrievalEventFailure(rep.RetrievalPhase, rootCid, peerID, address.Undef, err.Error()))
		return nil, err
	}

	duration := time.Since(startTime)
	speed := uint64(float64(state.Received()) / duration.Seconds())

	// Otherwise publish a retrieval event success
	fc.retrievalEventPublisher.Publish(rep.NewRetrievalEventSuccess(rep.RetrievalPhase, rootCid, peerID, address.Undef, state.Received(), state.ReceivedCidsTotal(), duration, totalPayment))

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

func (fc *FilClient) SubscribeToRetrievalEvents(subscriber rep.RetrievalSubscriber) {
	fc.retrievalEventPublisher.Subscribe(subscriber)
}

// Implement RetrievalSubscriber
func (fc *FilClient) OnRetrievalEvent(event rep.RetrievalEvent) {
	kv := make([]interface{}, 0)
	logadd := func(kva ...interface{}) {
		if len(kva)%2 != 0 {
			panic("bad number of key/value arguments")
		}
		for i := 0; i < len(kva); i += 2 {
			key, ok := kva[i].(string)
			if !ok {
				panic("expected string key")
			}
			kv = append(kv, key, kva[i+1])
		}
	}
	logadd("code", event.Code(),
		"phase", event.Phase(),
		"payloadCid", event.PayloadCid(),
		"storageProviderId", event.StorageProviderId(),
		"storageProviderAddr", event.StorageProviderAddr())
	switch tevent := event.(type) {
	case rep.RetrievalEventQueryAsk:
		logadd("queryResponse:Status", tevent.QueryResponse().Status,
			"queryResponse:PieceCIDFound", tevent.QueryResponse().PieceCIDFound,
			"queryResponse:Size", tevent.QueryResponse().Size,
			"queryResponse:PaymentAddress", tevent.QueryResponse().PaymentAddress,
			"queryResponse:MinPricePerByte", tevent.QueryResponse().MinPricePerByte,
			"queryResponse:MaxPaymentInterval", tevent.QueryResponse().MaxPaymentInterval,
			"queryResponse:MaxPaymentIntervalIncrease", tevent.QueryResponse().MaxPaymentIntervalIncrease,
			"queryResponse:Message", tevent.QueryResponse().Message,
			"queryResponse:UnsealPrice", tevent.QueryResponse().UnsealPrice)
	case rep.RetrievalEventFailure:
		logadd("errorMessage", tevent.ErrorMessage())
	case rep.RetrievalEventSuccess:
		logadd("receivedSize", tevent.ReceivedSize())
	}
	retrievalLogger.Debugw("retrieval-event", kv...)
}
