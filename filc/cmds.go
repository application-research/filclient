package main

import (
	"fmt"
	"os"
	"time"

	"github.com/filecoin-project/go-address"
	cborutil "github.com/filecoin-project/go-cbor-util"
	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/filecoin-project/go-fil-markets/storagemarket"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/lotus/chain/types"
	lcli "github.com/filecoin-project/lotus/cli"
	"github.com/ipfs/go-blockservice"
	"github.com/ipfs/go-cid"
	chunker "github.com/ipfs/go-ipfs-chunker"
	offline "github.com/ipfs/go-ipfs-exchange-offline"
	files "github.com/ipfs/go-ipfs-files"
	"github.com/ipfs/go-merkledag"
	unixfile "github.com/ipfs/go-unixfs/file"
	"github.com/ipfs/go-unixfs/importer"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	cli "github.com/urfave/cli/v2"
	"golang.org/x/xerrors"
)

var makeDealCmd = &cli.Command{
	Name:      "deal",
	Usage:     "Make a storage deal with a miner",
	ArgsUsage: "<file path>",
	Flags: []cli.Flag{
		flagMinerRequired,
		flagVerified,
	},
	Action: func(cctx *cli.Context) error {
		if !cctx.Args().Present() {
			return fmt.Errorf("please specify file to make deal for")
		}

		ddir := ddir(cctx)

		miner, err := parseMiner(cctx)
		if err != nil {
			return err
		}

		nd, err := setup(cctx.Context, ddir)
		if err != nil {
			return err
		}

		fc, closer, err := clientFromNode(cctx, nd, ddir)
		if err != nil {
			return err
		}
		defer closer()

		fi, err := os.Open(cctx.Args().First())
		if err != nil {
			return err
		}

		tpr := func(s string, args ...interface{}) {
			fmt.Printf("[%s] "+s+"\n", append([]interface{}{time.Now().Format("15:04:05")}, args...)...)
		}

		bserv := blockservice.New(nd.Blockstore, nil)
		dserv := merkledag.NewDAGService(bserv)

		tpr("importing file...")
		spl := chunker.DefaultSplitter(fi)

		obj, err := importer.BuildDagFromReader(dserv, spl)
		if err != nil {
			return err
		}

		tpr("File CID: %s", obj.Cid())

		ask, err := fc.GetAsk(cctx.Context, miner)
		if err != nil {
			return err
		}

		verified := parseVerified(cctx)

		price := ask.Ask.Ask.Price
		if verified {
			price = ask.Ask.Ask.VerifiedPrice
		}

		proposal, err := fc.MakeDeal(cctx.Context, miner, obj.Cid(), price, 0, 2880*365, verified)
		if err != nil {
			return err
		}

		propnd, err := cborutil.AsIpld(proposal.DealProposal)
		if err != nil {
			return xerrors.Errorf("failed to compute deal proposal ipld node: %w", err)
		}

		tpr("proposal cid: %s", propnd.Cid())

		if err := saveDealProposal(ddir, propnd.Cid(), proposal.DealProposal); err != nil {
			return err
		}

		resp, err := fc.SendProposal(cctx.Context, proposal)
		if err != nil {
			return err
		}

		tpr("response state: %d", resp.Response.State)
		switch resp.Response.State {
		case storagemarket.StorageDealError:
			return fmt.Errorf("error response from miner: %s", resp.Response.Message)
		case storagemarket.StorageDealProposalRejected:
			return fmt.Errorf("deal rejected by miner: %s", resp.Response.Message)
		default:
			return fmt.Errorf("unrecognized response from miner: %d %s", resp.Response.State, resp.Response.Message)
		case storagemarket.StorageDealWaitingForData, storagemarket.StorageDealProposalAccepted:
			tpr("miner accepted the deal!")
		}

		tpr("starting data transfer... %s", resp.Response.Proposal)

		chanid, err := fc.StartDataTransfer(cctx.Context, miner, resp.Response.Proposal, obj.Cid())
		if err != nil {
			return err
		}

		var lastStatus datatransfer.Status
	loop:
		for {
			status, err := fc.TransferStatus(cctx.Context, chanid)
			if err != nil {
				return err
			}

			switch status.Status {
			case datatransfer.Failed:
				return fmt.Errorf("data transfer failed: %s", status.Message)
			case datatransfer.Cancelled:
				return fmt.Errorf("transfer cancelled: %s", status.Message)
			case datatransfer.Failing:
				tpr("data transfer failing... %s", status.Message)
				// I guess we just wait until its failed all the way?
			case datatransfer.Requested:
				if lastStatus != status.Status {
					tpr("data transfer requested")
				}
				//fmt.Println("transfer is requested, hasnt started yet")
				// probably okay
			case datatransfer.TransferFinished, datatransfer.Finalizing, datatransfer.Completing:
				if lastStatus != status.Status {
					tpr("current state: %s", status.StatusStr)
				}
			case datatransfer.Completed:
				tpr("transfer complete!")
				break loop
			case datatransfer.Ongoing:
				fmt.Printf("[%s] transfer progress: %d      \n", time.Now().Format("15:04:05"), status.Sent)
			default:
				tpr("Unexpected data transfer state: %d (msg = %s)", status.Status, status.Message)
			}
			time.Sleep(time.Millisecond * 100)
			lastStatus = status.Status
		}

		tpr("transfer completed, miner: %s, propcid: %s %s", miner, resp.Response.Proposal, propnd.Cid())

		return nil
	},
}

var infoCmd = &cli.Command{
	Name:      "info",
	Usage:     "Display wallet information",
	ArgsUsage: " ",
	Action: func(cctx *cli.Context) error {
		ddir := ddir(cctx)

		nd, err := setup(cctx.Context, ddir)
		if err != nil {
			return err
		}

		api, closer, err := lcli.GetGatewayAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		addr, err := nd.Wallet.GetDefault()
		if err != nil {
			return err
		}

		balance := big.NewInt(0)
		verifiedBalance := big.NewInt(0)

		act, err := api.StateGetActor(cctx.Context, addr, types.EmptyTSK)
		if err != nil {
			fmt.Println("NOTE - Actor not found on chain")
		} else {
			balance = act.Balance

			v, err := api.StateVerifiedClientStatus(cctx.Context, addr, types.EmptyTSK)
			if err != nil {
				return err
			}

			verifiedBalance = *v
		}

		fmt.Printf("Default client address: %v\n", addr)
		fmt.Printf("Balance:                %v\n", types.FIL(balance))
		fmt.Printf("Verified Balance:       %v\n", types.FIL(verifiedBalance))

		return nil
	},
}

var getAskCmd = &cli.Command{
	Name:      "get-ask",
	Usage:     "Query storage deal ask for a miner",
	ArgsUsage: "<miner>",
	Action: func(cctx *cli.Context) error {
		if !cctx.Args().Present() {
			return fmt.Errorf("please specify miner to query ask of")
		}

		ddir := ddir(cctx)

		miner, err := address.NewFromString(cctx.Args().First())
		if err != nil {
			return err
		}

		fc, closer, err := getClient(cctx, ddir)
		if err != nil {
			return err
		}
		defer closer()

		ask, err := fc.GetAsk(cctx.Context, miner)
		if err != nil {
			return fmt.Errorf("failed to get ask: %s", err)
		}

		printAskResponse(ask.Ask.Ask)

		return nil
	},
}

var listDealsCmd = &cli.Command{
	Name:      "list",
	Usage:     "List local storage deal history",
	ArgsUsage: " ",
	Action: func(cctx *cli.Context) error {
		ddir := ddir(cctx)

		deals, err := listDeals(ddir)
		if err != nil {
			return err
		}

		for _, dcid := range deals {
			fmt.Println(dcid)
		}

		return nil
	},
}

var retrieveFileCmd = &cli.Command{
	Name:        "retrieve",
	Usage:       "Retrieve a file by CID from a miner",
	Description: "Retrieve a file by CID from a miner. If desired, multiple miners can be specified as fallbacks in case of a failure (comma-separated, no spaces).",
	ArgsUsage:   "<cid>",
	Flags: []cli.Flag{
		flagMiners,
		flagOutput,
		flagNoIPFS,
	},
	Action: func(cctx *cli.Context) error {

		// Parse command input

		cidStr := cctx.Args().First()
		if cidStr == "" {
			return fmt.Errorf("please specify a CID to retrieve")
		}

		miners, err := parseMiners(cctx)
		if err != nil {
			return err
		}

		output, err := parseOutput(cctx)
		if err != nil {
			return err
		}
		if output == "" {
			output = cidStr
		}

		noIPFS := cctx.Bool(flagNoIPFS.Name)
		println("NO IPFS:", noIPFS)

		c, err := cid.Decode(cidStr)
		if err != nil {
			return err
		}

		// Set up node and filclient

		ddir := ddir(cctx)

		node, err := setup(cctx.Context, ddir)
		if err != nil {
			return err
		}

		fc, closer, err := clientFromNode(cctx, node, ddir)
		if err != nil {
			return err
		}
		defer closer()

		// Collect retrieval candidates and config. If one or more miners are
		// provided, use those with the requested cid as the root cid as the
		// candidate list. Otherwise, we can use the auto retrieve API endpoint
		// to automatically find some candidates to retrieve from.

		var candidates []RetrievalCandidate
		if len(miners) > 0 {
			for _, miner := range miners {
				candidates = append(candidates, RetrievalCandidate{
					Miner:   miner,
					RootCid: c,
				})
			}
		} else {
			endpoint := "https://api.estuary.tech/retrieval-candidates" // TODO: don't hard code
			candidates_, err := node.GetRetrievalCandidates(endpoint, c)
			if err != nil {
				return err
			}

			candidates = candidates_
		}

		// Do the retrieval

		stats, err := node.RetrieveFromBestCandidate(cctx.Context, fc, c, candidates, CandidateSelectionConfig{
			tryIPFS: !noIPFS,
		})
		if err != nil {
			return err
		}

		printRetrievalStats(stats)

		// Save the output

		dservOffline := merkledag.NewDAGService(blockservice.New(node.Blockstore, offline.Exchange(node.Blockstore)))

		dnode, err := dservOffline.Get(cctx.Context, c)
		if err != nil {
			return err
		}

		ufsFile, err := unixfile.NewUnixfsFile(cctx.Context, dservOffline, dnode)
		if err != nil {
			return err
		}

		if err := files.WriteTo(ufsFile, output); err != nil {
			return err
		}

		fmt.Println("Saved output to", output)

		return nil
	},
}

var queryRetrievalCmd = &cli.Command{
	Name:      "query-retrieval",
	Usage:     "Query retrieval information for a CID",
	ArgsUsage: "<cid>",
	Flags: []cli.Flag{
		flagMiner,
	},
	Action: func(cctx *cli.Context) error {

		cidStr := cctx.Args().First()
		if cidStr == "" {
			return fmt.Errorf("please specify a CID to query retrieval of")
		}

		miner, err := parseMiner(cctx)
		if err != nil {
			return err
		}

		cid, err := cid.Decode(cidStr)
		if err != nil {
			return err
		}

		ddir := ddir(cctx)

		nd, err := setup(cctx.Context, ddir)
		if err != nil {
			return err
		}

		dht, err := dht.New(cctx.Context, nd.Host, dht.Mode(dht.ModeClient))
		if err != nil {
			return err
		}

		providers, err := dht.FindProviders(cctx.Context, cid)
		if err != nil {
			return err
		}

		availableOnIPFS := len(providers) != 0

		if miner != address.Undef {
			fc, closer, err := clientFromNode(cctx, nd, ddir)
			if err != nil {
				return err
			}
			defer closer()

			query, err := fc.RetrievalQuery(cctx.Context, miner, cid)
			if err != nil {
				return err
			}

			printQueryResponse(query, availableOnIPFS)
		} else {
			fmt.Println("No miner specified")
			if availableOnIPFS {
				fmt.Println("Available on IPFS")
			}
		}

		return nil
	},
}

var clearBlockstoreCmd = &cli.Command{
	Name:      "clear-blockstore",
	Usage:     "Delete all retrieved file data in the blockstore",
	ArgsUsage: " ",
	Action: func(cctx *cli.Context) error {
		ddir := ddir(cctx)

		fmt.Println("clearing blockstore...")

		if err := os.RemoveAll(blockstorePath(ddir)); err != nil {
			return err
		}

		fmt.Println("done")

		return nil
	},
}
