package main

import (
	"fmt"

	"github.com/dustin/go-humanize"
	"github.com/filecoin-project/go-fil-markets/retrievalmarket"
	"github.com/filecoin-project/go-fil-markets/storagemarket"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/lotus/chain/types"
)

func printAskResponse(ask *storagemarket.StorageAsk) {
	fmt.Printf(`ASK RESPONSE
-----
Miner: %v
Price (Unverified): %v (%v)
Price (Verified): %v (%v)
Min Piece Size: %v
Max Piece Size: %v
`,
		ask.Miner,
		ask.Price, types.FIL(ask.Price),
		ask.VerifiedPrice, types.FIL(ask.VerifiedPrice),
		ask.MinPieceSize,
		ask.MaxPieceSize,
	)
}

func printDealStatus(state *storagemarket.ProviderDealState) {
	fmt.Printf(`DEAL STATUS
-----
Deal State:     %s
Proposal CID:   %s
Add Funds CID:  %s
Publish CID:    %s
Deal ID:        %d
Fast Retrieval: %t
`,
		storagemarket.DealStates[state.State],
		state.ProposalCid,
		state.AddFundsCid,
		state.PublishCid,
		state.DealID,
		state.FastRetrieval,
	)

	if state.Proposal != nil {
		fmt.Printf(`Proposal:
	Piece CID:               %s
	Piece Size:              %d (%s)
	Verified Deal:           %t
	Client:                  %s
	Provider:                %s
	Label:                   %s
	Start Epoch:             %d
	End Epoch:               %d
	Storage Price Per Epoch: %d (%s)
	Provider Collateral:     %d (%s)
	Client Collateral:       %d (%d)
`,
			state.Proposal.PieceCID,
			state.Proposal.PieceSize, humanize.IBytes(uint64(state.Proposal.PieceSize)),
			state.Proposal.VerifiedDeal,
			state.Proposal.Client,
			state.Proposal.Provider,
			state.Proposal.Label,
			state.Proposal.StartEpoch,
			state.Proposal.EndEpoch,
			state.Proposal.StoragePricePerEpoch, types.FIL(state.Proposal.StoragePricePerEpoch),
			state.Proposal.ProviderCollateral, types.FIL(state.Proposal.ProviderCollateral),
			state.Proposal.ClientCollateral, types.FIL(state.Proposal.ClientCollateral),
		)
	}

	if state.Message != "" {
		fmt.Printf("Message: %s\n", state.Message)
	}
}

func printRetrievalStats(stats RetrievalStats) {
	switch stats := stats.(type) {
	case *FILRetrievalStats:
		fmt.Printf(`RETRIEVAL STATS (FIL)
-----
Size:          %v (%v)
Duration:      %v
Average Speed: %v (%v/s)
Ask Price:     %v (%v)
Total Payment: %v (%v)
Num Payments:  %v
Peer:          %v
`,
			stats.Size, humanize.IBytes(stats.Size),
			stats.Duration,
			stats.AverageSpeed, humanize.IBytes(stats.AverageSpeed),
			stats.AskPrice, types.FIL(stats.AskPrice),
			stats.TotalPayment, types.FIL(stats.TotalPayment),
			stats.NumPayments,
			stats.Peer,
		)
	case *IPFSRetrievalStats:
		fmt.Printf(`RETRIEVAL STATS (IPFS)
-----
Size:          %v (%v)
Duration:      %v
Average Speed: %v
`,
			stats.ByteSize, humanize.IBytes(stats.ByteSize),
			stats.Duration,
			stats.GetAverageBytesPerSecond(),
		)
	}
}

func printQueryResponse(query *retrievalmarket.QueryResponse, availableOnIPFS bool) {
	var status string
	switch query.Status {
	case retrievalmarket.QueryResponseAvailable:
		status = "Available"
	case retrievalmarket.QueryResponseUnavailable:
		status = "Unavailable"
	case retrievalmarket.QueryResponseError:
		status = "Error"
	default:
		status = fmt.Sprintf("Unrecognized Status (%d)", query.Status)
	}

	var pieceCIDFound string
	switch query.PieceCIDFound {
	case retrievalmarket.QueryItemAvailable:
		pieceCIDFound = "Available"
	case retrievalmarket.QueryItemUnavailable:
		pieceCIDFound = "Unavailable"
	case retrievalmarket.QueryItemUnknown:
		pieceCIDFound = "Unknown"
	default:
		pieceCIDFound = fmt.Sprintf("Unrecognized (%d)", query.PieceCIDFound)
	}

	total := big.Add(query.UnsealPrice, big.Mul(big.NewIntUnsigned(query.Size), query.MinPricePerByte))
	fmt.Printf(`QUERY RESPONSE
-----
Status:                        %v
Piece CID Found:               %v
Size:                          %v (%v)
Unseal Price:                  %v (%v)
Min Price Per Byte:            %v (%v)
Total Retrieval Price:         %v (%v)
Payment Address:               %v
Max Payment Interval:          %v (%v)
Max Payment Interval Increase: %v (%v)
`,
		status,
		pieceCIDFound,
		query.Size, humanize.IBytes(query.Size),
		query.UnsealPrice, types.FIL(query.UnsealPrice),
		query.MinPricePerByte, types.FIL(query.MinPricePerByte),
		total, types.FIL(total),
		query.PaymentAddress,
		query.MaxPaymentInterval, humanize.IBytes(query.MaxPaymentInterval),
		query.MaxPaymentIntervalIncrease, humanize.IBytes(query.MaxPaymentIntervalIncrease),
	)

	if query.Message != "" {
		fmt.Printf("Message: %v\n", query.Message)
	}

	if availableOnIPFS {
		fmt.Printf("-----\nAvaiable on IPFS")
	}
}
