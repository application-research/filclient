package filclient

import (
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/filecoin-project/go-address"
	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/filecoin-project/go-fil-markets/storagemarket"
	"github.com/filecoin-project/go-jsonrpc"
	"github.com/filecoin-project/lotus/api"
	lcli "github.com/filecoin-project/lotus/cli"
	"github.com/filecoin-project/lotus/itests/kit"
	"github.com/ipfs/go-blockservice"
	"github.com/ipfs/go-datastore"
	flatfs "github.com/ipfs/go-ds-flatfs"
	leveldb "github.com/ipfs/go-ds-leveldb"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	chunk "github.com/ipfs/go-ipfs-chunker"
	logging "github.com/ipfs/go-log/v2"
	"github.com/ipfs/go-merkledag"
	"github.com/ipfs/go-unixfs/importer"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/urfave/cli/v2"
)

type DummyDataGen struct {
	TargetByteLen uint64
	progress      uint64
}

func NewDummyDataGen(targetByteLen uint64) *DummyDataGen {
	return &DummyDataGen{
		TargetByteLen: targetByteLen,
		progress:      0,
	}
}

func (reader *DummyDataGen) Read(p []byte) (int, error) {
	i := 0
	for ; i < len(p); i += 1 {
		// End if we read enough bytes
		if reader.progress >= reader.TargetByteLen {
			return i, io.EOF
		}

		// Otherwise write the next byte
		p[i] = byte(rand.Uint32() % 0xff)

		reader.progress += 1
	}

	return i, nil
}

func TestMain(t *testing.T) {

	logging.SetLogLevel("*", "error")

	app := cli.NewApp()

	os.Setenv("FULLNODE_API_INFO", "")

	app.Action = func(cctx *cli.Context) error {

		// Test network initialization

		fmt.Printf("Initializing test network...\n")

		client, miner, ensemble := kit.EnsembleMinimal(t)
		ensemble.InterconnectAll().BeginMining(250 * time.Millisecond)

		// _ = kit.NewDealHarness(t, client, miner, miner)

		fmt.Printf("Test network running on %s\n", client.ListenAddr)

		// FilClient initialization

		fmt.Printf("Initializing filclient...\n")

		h := initHost(t)
		api, closer := initAPI(t, cctx)
		// addr := initAddress(t)
		bs := initBlockstore(t)
		ds := initDatastore(t)
		defer closer()
		fc, err := NewClient(h, api, nil, address.Undef, bs, ds, t.TempDir())
		if err != nil {
			t.Fatalf("Could not initialize FilClient: %v", err)
		}

		// Test data initialization

		fmt.Printf("Initializing test data...\n")

		bserv := blockservice.New(bs, nil)
		dserv := merkledag.NewDAGService(bserv)

		const gib = 128 * 1024 * 1024
		spl := chunk.DefaultSplitter(NewDummyDataGen(gib))

		obj, err := importer.BuildDagFromReader(dserv, spl)
		if err != nil {
			t.Fatalf("Could not build test data DAG: %v", err)
		}
		// piece, err := client.ClientDealPieceCID(cctx.Context, obj.Cid())
		// if err != nil {
		// 	t.Fatalf("Could not get test data piece CID: %v", err)
		// }

		// dp := dh.DefaultStartDealParams()
		// dp.DealStartEpoch = abi.ChainEpoch(4 << 10)
		// dp.Data = &storagemarket.DataRef{
		// 	TransferType: storagemarket.TTManual,
		// 	Root:         obj.Cid(),
		// 	PieceCid:     &piece.PieceCID,
		// 	PieceSize:    piece.PieceSize.Unpadded(),
		// }
		// dh.StartDeal(cctx.Context, dp)

		fmt.Printf("Starting tests...\n")

		// Tests

		t.Run("storage", func(t *testing.T) {
			ask, err := fc.GetAsk(cctx.Context, miner.ActorAddr)

			proposal, err := fc.MakeDeal(cctx.Context, miner.ActorAddr, obj.Cid(), ask.Ask.Ask.Price, 0, 2880*365, false)
			if err != nil {
				t.Fatalf("Failed to make deal with miner: %v", err)
			}

			// proposalNode, err := cborutil.AsIpld(proposal.DealProposal)
			// if err != nil {
			// 	t.Fatalf("Failed to get proposal node: %v", err)
			// }

			proposalRes, err := fc.SendProposal(cctx.Context, proposal)
			if err != nil {
				t.Fatalf("Sending proposal failed: %v", err)
			}

			switch proposalRes.Response.State {
			case storagemarket.StorageDealWaitingForData, storagemarket.StorageDealProposalAccepted:
			default:
				t.Fatalf("Test deal was not accepted")
			}

			var chanid datatransfer.ChannelID
			var chanidLk sync.Mutex
			res := make(chan error, 1)

			finish := func(err error) {
				select {
				case res <- err:
				default:
				}
			}

			unsubscribe := fc.SubscribeToDataTransferEvents(func(event datatransfer.Event, state datatransfer.ChannelState) {
				chanidLk.Lock()
				chanidCopy := chanid
				chanidLk.Unlock()

				// Skip messages not related to this channel
				if state.ChannelID() != chanidCopy {
					return
				}

				switch event.Code {
				case datatransfer.Complete:
					finish(nil)
				case datatransfer.Error:
					finish(fmt.Errorf("data transfer failed"))
				}
			})
			defer unsubscribe()

			chanidLk.Lock()
			chanid_, err := fc.StartDataTransfer(cctx.Context, miner.ActorAddr, proposalRes.Response.Proposal, obj.Cid())
			chanid = *chanid_
			chanidLk.Unlock()
			if err != nil {
				t.Fatalf("Failed to start data transfer")
			}

			select {
			case err := <-res:
				if err != nil {
					t.Fatalf("Data transfer error: %v", err)
				}
			case <-cctx.Done():
			}

			if err := fc.dataTransfer.CloseDataTransferChannel(cctx.Context, chanid); err != nil {
				t.Fatalf("Failed to close data transfer channel")
			}
		})

		t.Run("retrieval", func(t *testing.T) {
			// qres, err := fc.RetrievalQuery(cctx.Context, miner.ActorAddr, obj.Cid())
			// if err != nil {
			// 	t.Fatalf("Retrieval query failed: %v", err)
			// }

			// fmt.Printf("Retrieval query result: %#v", qres)
		})

		return nil
	}

	if err := app.Run([]string{""}); err != nil {
		t.Fatalf("App failed: %v", err)
	}
}

// -- Setup functions

func initHost(t *testing.T) host.Host {
	h, err := libp2p.New()
	if err != nil {
		t.Fatalf("Could not initialize libp2p: %v", err)
	}

	return h
}

func initAPI(t *testing.T, cctx *cli.Context) (api.Gateway, jsonrpc.ClientCloser) {
	api, closer, err := lcli.GetGatewayAPI(cctx)
	if err != nil {
		t.Fatalf("Could not initialize Lotus API gateway: %v", err)
	}

	return api, closer
}

func initAddress(t *testing.T) address.Address {
	addr, err := address.NewFromString("127.0.0.1")
	if err != nil {
		t.Fatalf("Could not initialize address: %v", err)
	}

	return addr
}

func initBlockstore(t *testing.T) blockstore.Blockstore {
	parseShardFunc, err := flatfs.ParseShardFunc("/repo/flatfs/shard/v1/next-to-last/3")
	if err != nil {
		t.Fatalf("Blockstore parse shard func failed: %v", err)
	}

	ds, err := flatfs.CreateOrOpen(filepath.Join(t.TempDir(), "blockstore"), parseShardFunc, false)
	if err != nil {
		t.Fatalf("Could not initialize blockstore: %v", err)
	}

	bs := blockstore.NewBlockstoreNoPrefix(ds)

	return bs
}

func initDatastore(t *testing.T) datastore.Batching {
	ds, err := leveldb.NewDatastore(filepath.Join(t.TempDir(), "datastore"), nil)
	if err != nil {
		t.Fatalf("Could not initialize datastore: %v", err)
	}

	return ds
}
