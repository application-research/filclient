package main

import (
	"context"
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/application-research/filclient"
	"github.com/application-research/filclient/retrievehelper"
	"github.com/filecoin-project/boost/transport/httptransport"
	"github.com/filecoin-project/go-address"
	cborutil "github.com/filecoin-project/go-cbor-util"
	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/filecoin-project/go-fil-markets/storagemarket/network"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/lotus/chain/types"
	lcli "github.com/filecoin-project/lotus/cli"
	"github.com/google/uuid"
	"github.com/ipfs/go-blockservice"
	"github.com/ipfs/go-cid"
	chunker "github.com/ipfs/go-ipfs-chunker"
	offline "github.com/ipfs/go-ipfs-exchange-offline"
	files "github.com/ipfs/go-ipfs-files"
	logging "github.com/ipfs/go-log/v2"
	"github.com/ipfs/go-merkledag"
	unixfile "github.com/ipfs/go-unixfs/file"
	"github.com/ipfs/go-unixfs/importer"
	"github.com/ipld/go-car"
	"github.com/ipld/go-ipld-prime"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	basicnode "github.com/ipld/go-ipld-prime/node/basic"
	"github.com/ipld/go-ipld-prime/traversal"
	"github.com/ipld/go-ipld-prime/traversal/selector"
	"github.com/ipld/go-ipld-prime/traversal/selector/builder"
	textselector "github.com/ipld/go-ipld-selector-text-lite"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	inet "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	"github.com/urfave/cli/v2"
	"golang.org/x/xerrors"
)

var printLoggersCmd = &cli.Command{
	Name:  "print-loggers",
	Usage: "Display loggers present in the program to help configure log levels",
	Action: func(cctx *cli.Context) error {
		loggers := logging.GetSubsystems()

		for _, logger := range loggers {
			fmt.Printf("%s\n", logger)
		}

		return nil
	},
}

func tpr(s string, args ...interface{}) {
	fmt.Printf("[%s] "+s+"\n", append([]interface{}{time.Now().Format("15:04:05")}, args...)...)
}

var makeDealCmd = &cli.Command{
	Name:      "deal",
	Usage:     "Make a storage deal with a miner",
	ArgsUsage: "<file path>",
	Flags: []cli.Flag{
		flagMinerRequired,
		flagVerified,
		&cli.StringFlag{
			Name:  "announce",
			Usage: "the public multi-address from which to download the data (for deals with protocol v120)",
		},
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

		bserv := blockservice.New(nd.Blockstore, nil)
		dserv := merkledag.NewDAGService(bserv)

		tpr("importing file...")
		spl := chunker.DefaultSplitter(fi)

		obj, err := importer.BuildDagFromReader(dserv, spl)
		if err != nil {
			return err
		}

		tpr("File CID: %s", obj.Cid())

		tpr("getting ask from storage provider %s...", miner)
		ask, err := fc.GetAsk(cctx.Context, miner)
		if err != nil {
			return fmt.Errorf("getting ask from storage provider %s: %w", miner, err)
		}

		verified := parseVerified(cctx)
		removeUnsealed := parseRemoveUnsealed(cctx)

		price := ask.Ask.Ask.Price
		if verified {
			price = ask.Ask.Ask.VerifiedPrice
			tpr("storage provider ask for verified deals: %d", price)
		} else {
			tpr("storage provider ask: %d", price)
		}

		minPieceSize := ask.Ask.Ask.MinPieceSize
		proposal, err := fc.MakeDeal(cctx.Context, miner, obj.Cid(), price, minPieceSize, 2880*365, verified, removeUnsealed)
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

		proto, err := fc.DealProtocolForMiner(cctx.Context, miner)
		if err != nil {
			return err
		}

		tpr("storage provider supports deal protocol %s", proto)

		switch {
		case proto == filclient.DealProtocolv110:
			return makev110Deal(cctx, fc, miner, proposal, propnd.Cid(), obj.Cid())
		case proto == filclient.DealProtocolv120:
			return makev120Deal(cctx, fc, nd.Host, miner, proposal, propnd.Cid())
		default:
			return fmt.Errorf("unrecognized deal protocol %s", proto)
		}
	},
}

func makev110Deal(cctx *cli.Context, fc *filclient.FilClient, miner address.Address, proposal *network.Proposal, propCid cid.Cid, dataCid cid.Cid) error {
	ctx := cctx.Context

	// Send the deal proposal
	_, err := fc.SendProposalV110(ctx, *proposal, propCid)
	if err != nil {
		return err
	}

	tpr("miner accepted the deal!")

	// Start the push transfer
	tpr("starting data transfer... %s", propCid)
	chanid, err := fc.StartDataTransfer(ctx, miner, propCid, dataCid)
	if err != nil {
		return err
	}

	// Periodically check the transfer status and output a log
	var lastStatus datatransfer.Status
	for {
		status, err := fc.TransferStatus(ctx, chanid)
		if err != nil {
			return err
		}

		statusChanged := status.Status != lastStatus
		logstr, err := logStatus(status, statusChanged)
		if err != nil {
			return err
		}
		if logstr != "" {
			tpr(logstr)
		}
		if status.Status == datatransfer.Completed {
			tpr("transfer completed, miner: %s, propcid: %s", miner, propCid)
			return nil
		}
		lastStatus = status.Status

		time.Sleep(time.Millisecond * 100)
	}
	return nil
}

func makev120Deal(cctx *cli.Context, fc *filclient.FilClient, h host.Host, miner address.Address, netprop *network.Proposal, propCid cid.Cid) error {
	var announceAddr multiaddr.Multiaddr
	tpr("filc host addr: %s", h.Addrs())
	tpr("filc host peer: %s", h.ID())
	announce := cctx.String("announce")
	if announce == "" {
		return fmt.Errorf("must specify announce address to make deals over deal v1.2.0 protocol %s", filclient.DealProtocolv120)
	}

	announceStr := announce + "/p2p/" + h.ID().String()
	announceAddr, err := multiaddr.NewMultiaddr(announceStr)
	if err != nil {
		return fmt.Errorf("parsing announce address '%s': %w", announceStr, err)
	}
	tpr("filc announce address: %s", announceAddr.String())

	dbid := uint(rand.Uint32())
	dealUUID := uuid.New()
	pullComplete := make(chan error)
	var lastStatus datatransfer.Status

	// Subscribe to pull transfer updates.
	unsubPullEvts, err := fc.Libp2pTransferMgr.Subscribe(func(evtdbid uint, st filclient.ChannelState) {
		if dbid != evtdbid {
			return
		}

		statusChanged := st.Status != lastStatus
		logstr, err := logStatus(&st, statusChanged)
		if err != nil {
			pullComplete <- err
			return
		}

		if logstr != "" {
			tpr(logstr)
		}

		if st.Status == datatransfer.Completed {
			tpr("transfer completed, miner: %s, propcid: %s", miner, propCid)
			pullComplete <- nil
		}

		lastStatus = st.Status
	})
	if err != nil {
		return err
	}
	defer unsubPullEvts()

	// Keep the connection alive
	ctx, cancel := context.WithCancel(cctx.Context)
	defer cancel()
	go keepConnection(ctx, fc, h, miner, tpr)

	// In deal protocol v120 the transfer will be initiated by the
	// storage provider (a pull transfer) so we need to prepare for
	// the data request
	tpr("sending v1.2.0 deal proposal with dbid %d, deal uuid %s", dbid, dealUUID.String())

	// Create an auth token to be used in the request
	authToken, err := httptransport.GenerateAuthToken()
	if err != nil {
		return xerrors.Errorf("generating auth token for deal: %w", err)
	}

	// Add an auth token for the data to the auth DB
	rootCid := netprop.Piece.Root
	size := netprop.Piece.RawBlockSize
	err = fc.Libp2pTransferMgr.PrepareForDataRequest(ctx, dbid, authToken, propCid, rootCid, size)
	if err != nil {
		return xerrors.Errorf("preparing for data request: %w", err)
	}

	// Send the deal proposal to the storage provider
	_, err = fc.SendProposalV120(ctx, dbid, *netprop, dealUUID, announceAddr, authToken)
	if err != nil {
		// Clean up auth token
		fc.Libp2pTransferMgr.CleanupPreparedRequest(ctx, dbid, authToken) //nolint:errcheck
		return err
	}

	tpr("miner accepted the deal!")

	// Wait for the transfer to complete (while outputting logs)
	select {
	case <-cctx.Context.Done():
		return cctx.Context.Err()
	case err = <-pullComplete:
	}
	return err
}

func logStatus(status *filclient.ChannelState, changed bool) (string, error) {
	switch status.Status {
	case datatransfer.Failed:
		return "", fmt.Errorf("data transfer failed: %s", status.Message)
	case datatransfer.Cancelled:
		return "", fmt.Errorf("transfer cancelled: %s", status.Message)
	case datatransfer.Failing:
		return fmt.Sprintf("data transfer failing... %s", status.Message), nil
		// I guess we just wait until its failed all the way?
	case datatransfer.Requested:
		if changed {
			return "data transfer requested", nil
		}
		//fmt.Println("transfer is requested, hasnt started yet")
		// probably okay
	case datatransfer.TransferFinished, datatransfer.Finalizing, datatransfer.Completing:
		if changed {
			return "current state: " + status.StatusStr, nil
		}
	case datatransfer.Completed:
		return "transfer complete!", nil
	case datatransfer.Ongoing:
		return fmt.Sprintf("transfer progress: %d", status.Sent), nil
	default:
		return fmt.Sprintf("Unexpected data transfer state: %d (msg = %s)", status.Status, status.Message), nil
	}
	return "", nil
}

// keepConnection watches the connection to the miner, reconnecting if it goes
// down
func keepConnection(ctx context.Context, fc *filclient.FilClient, host host.Host, maddr address.Address, tpr func(s string, args ...interface{})) {
	pid, err := fc.ConnectToMiner(ctx, maddr)
	if err != nil {
		tpr("Unable to make initial connection to storage provider %s: %s", maddr, err)
		return
	}

	tpr("Watching connection to storage provider %s with peer ID %s", maddr, pid)
	cw := connWatcher{
		pid:          pid,
		disconnected: make(chan struct{}, 1),
		reconnect: func() {
			tpr("Connection to storage provider %s disconnected. Reconnecting...", maddr)
			_, err := fc.ConnectToMiner(ctx, maddr)
			if err == nil {
				tpr("Reconnected to storage provider %s. Waiting for storage provider to restart transfer...", maddr)
			} else {
				tpr("Failed to reconnect to storage provider %s: %s", maddr, err)
			}
		},
	}
	host.Network().Notify(&cw)
}

type connWatcher struct {
	pid          peer.ID
	reconnect    func()
	disconnected chan struct{}
}

// Disconnected is called when a connection breaks
func (c *connWatcher) Disconnected(n inet.Network, conn inet.Conn) {
	if conn.RemotePeer() != c.pid {
		return
	}

	select {
	case c.disconnected <- struct{}{}:
		go c.processDisconnect()
	default:
	}
}

func (c *connWatcher) processDisconnect() {
	// Sleep for a few seconds to prevent flapping
	time.Sleep(5 * time.Second)

	select {
	case <-c.disconnected:
		c.reconnect()
		// Do one more reconnect in case a disconnect happened while we were
		// reconnecting,
		go c.processDisconnect()
	default:
	}
}

func (c *connWatcher) Listen(n inet.Network, m multiaddr.Multiaddr)      {}
func (c *connWatcher) ListenClose(n inet.Network, m multiaddr.Multiaddr) {}
func (c *connWatcher) Connected(n inet.Network, conn inet.Conn)          {}
func (c *connWatcher) OpenedStream(n inet.Network, stream inet.Stream)   {}
func (c *connWatcher) ClosedStream(n inet.Network, stream inet.Stream)   {}

var dealStatusCmd = &cli.Command{
	Name:      "deal-status",
	Usage:     "Get on-chain deal status",
	ArgsUsage: "<proposal cid>",
	Flags: []cli.Flag{
		flagMinerRequired,
		flagDealUUID,
	},
	Action: func(cctx *cli.Context) error {
		if !cctx.Args().Present() {
			return fmt.Errorf("proposal CID must be specified")
		}

		miner, err := parseMiner(cctx)
		if err != nil {
			return fmt.Errorf("invalid miner address: %w", err)
		}

		dealUUID, err := parseDealUUID(cctx)
		if err != nil {
			return fmt.Errorf("invalid deal UUID: %w", err)
		}

		cid, err := cid.Decode(cctx.Args().First())
		if err != nil {
			return fmt.Errorf("invalid proposal CID: %w", err)
		}

		nd, err := setup(cctx.Context, ddir(cctx))
		if err != nil {
			return fmt.Errorf("could not set up node: %w", err)
		}

		fc, closer, err := clientFromNode(cctx, nd, ddir(cctx))
		if err != nil {
			return fmt.Errorf("could not initialize filclient: %w", err)
		}
		defer closer()

		var dealUUIDPtr *uuid.UUID
		if dealUUID != uuid.Nil {
			dealUUIDPtr = &dealUUID
		}

		dealStatus, err := fc.DealStatus(cctx.Context, miner, cid, dealUUIDPtr)
		if err != nil {
			return fmt.Errorf("could not get deal state from provider: %w", err)
		}

		printDealStatus(dealStatus)

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
		flagNetwork,
		flagDmPathSel,
		flagCar,
	},
	Action: func(cctx *cli.Context) error {

		// Parse command input

		cidStr := cctx.Args().First()
		if cidStr == "" {
			return fmt.Errorf("please specify a CID to retrieve")
		}

		dmSelText := textselector.Expression(cctx.String(flagDmPathSel.Name))

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
			if dmSelText != "" {
				output += "_" + url.QueryEscape(string(dmSelText))
			}
		}

		network := strings.ToLower(strings.TrimSpace(cctx.String("network")))

		c, err := cid.Decode(cidStr)
		if err != nil {
			return err
		}

		// Get subselector node

		var selNode ipld.Node
		if dmSelText != "" {
			ssb := builder.NewSelectorSpecBuilder(basicnode.Prototype.Any)

			selspec, err := textselector.SelectorSpecFromPath(
				dmSelText,
				true,

				// URGH - this is a direct copy from https://github.com/filecoin-project/go-fil-markets/blob/v1.12.0/shared/selectors.go#L10-L16
				// Unable to use it because we need the SelectorSpec, and markets exposes just a reified node
				ssb.ExploreRecursive(
					selector.RecursionLimitNone(),
					ssb.ExploreAll(ssb.ExploreRecursiveEdge()),
				),
			)
			if err != nil {
				return xerrors.Errorf("failed to parse text-selector '%s': %w", dmSelText, err)
			}

			selNode = selspec.Node()
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

		var candidates []FILRetrievalCandidate
		if len(miners) > 0 {
			for _, miner := range miners {
				candidates = append(candidates, FILRetrievalCandidate{
					Miner:   miner,
					RootCid: c,
				})
			}
		} else {
			endpoint := "https://api.estuary.tech/retrieval-candidates" // TODO: don't hard code
			candidates_, err := node.GetRetrievalCandidates(endpoint, c)
			if err != nil {
				return fmt.Errorf("failed to get retrieval candidates: %w", err)
			}

			candidates = candidates_
		}

		// Do the retrieval

		var networks []RetrievalAttempt

		if network == NetworkIPFS || network == NetworkAuto {
			if selNode != nil && !selNode.IsNull() {
				// Selector nodes are not compatible with IPFS
				if network == NetworkIPFS {
					log.Fatal("IPFS is not compatible with selector node")
				} else {
					log.Info("A selector node has been specified, skipping IPFS")
				}
			} else {
				networks = append(networks, &IPFSRetrievalAttempt{
					Cid: c,
				})
			}
		}

		if network == NetworkFIL || network == NetworkAuto {
			networks = append(networks, &FILRetrievalAttempt{
				FilClient:  fc,
				Cid:        c,
				Candidates: candidates,
				SelNode:    selNode,
			})
		}

		if len(networks) == 0 {
			log.Fatalf("Unknown --network value \"%s\"", network)
		}

		stats, err := node.RetrieveFromBestCandidate(cctx.Context, networks)
		if err != nil {
			return err
		}

		printRetrievalStats(stats)

		// Save the output

		dservOffline := merkledag.NewDAGService(blockservice.New(node.Blockstore, offline.Exchange(node.Blockstore)))

		// if we used a selector - need to find the sub-root the user actually wanted to retrieve
		if dmSelText != "" {
			var subRootFound bool

			// no err check - we just compiled this before starting, but now we do not wrap a `*`
			selspec, _ := textselector.SelectorSpecFromPath(dmSelText, true, nil) //nolint:errcheck
			if err := retrievehelper.TraverseDag(
				cctx.Context,
				dservOffline,
				c,
				selspec.Node(),
				func(p traversal.Progress, n ipld.Node, r traversal.VisitReason) error {
					if r == traversal.VisitReason_SelectionMatch {

						if p.LastBlock.Path.String() != p.Path.String() {
							return xerrors.Errorf("unsupported selection path '%s' does not correspond to a node boundary (a.k.a. CID link)", p.Path.String())
						}

						cidLnk, castOK := p.LastBlock.Link.(cidlink.Link)
						if !castOK {
							return xerrors.Errorf("cidlink cast unexpectedly failed on '%s'", p.LastBlock.Link.String())
						}

						c = cidLnk.Cid
						subRootFound = true
					}
					return nil
				},
			); err != nil {
				return xerrors.Errorf("error while locating partial retrieval sub-root: %w", err)
			}

			if !subRootFound {
				return xerrors.Errorf("path selection '%s' does not match a node within %s", dmSelText, c)
			}
		}

		dnode, err := dservOffline.Get(cctx.Context, c)
		if err != nil {
			return err
		}

		if cctx.Bool(flagCar.Name) {
			// Write file as car file
			file, err := os.Create(output + ".car")
			if err != nil {
				return err
			}
			car.WriteCar(cctx.Context, dservOffline, []cid.Cid{c}, file)

			fmt.Println("Saved .car output to", output+".car")
		} else {
			// Otherwise write file as UnixFS File
			ufsFile, err := unixfile.NewUnixfsFile(cctx.Context, dservOffline, dnode)
			if err != nil {
				return err
			}

			if err := files.WriteTo(ufsFile, output); err != nil {
				return err
			}

			fmt.Println("Saved output to", output)
		}

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
