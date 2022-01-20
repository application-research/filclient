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

	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/filecoin-project/go-fil-markets/storagemarket"
	"github.com/filecoin-project/go-jsonrpc"
	"github.com/filecoin-project/lotus/api"
	lotusactors "github.com/filecoin-project/lotus/chain/actors"
	lotusminer "github.com/filecoin-project/lotus/chain/actors/builtin/miner"
	lotustypes "github.com/filecoin-project/lotus/chain/types"
	"github.com/filecoin-project/lotus/chain/wallet"
	lcli "github.com/filecoin-project/lotus/cli"
	"github.com/filecoin-project/lotus/itests/kit"
	lotusrepo "github.com/filecoin-project/lotus/node/repo"
	filminer "github.com/filecoin-project/specs-actors/v6/actors/builtin/miner"
	"github.com/ipfs/go-blockservice"
	"github.com/ipfs/go-datastore"
	flatfs "github.com/ipfs/go-ds-flatfs"
	leveldb "github.com/ipfs/go-ds-leveldb"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	chunk "github.com/ipfs/go-ipfs-chunker"
	logging "github.com/ipfs/go-log/v2"
	"github.com/ipfs/go-merkledag"
	"github.com/ipfs/go-unixfs/importer"
	"github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
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

	app.Action = func(cctx *cli.Context) error {

		ctx := cctx.Context

		// Test network initialization

		fmt.Printf("Initializing test network...\n")

		kit.EnableLargeSectors(t)
		kit.QuietMiningLogs()
		client, miner, ensemble := kit.EnsembleMinimal(t,
			kit.ThroughRPC(),        // so filclient can talk to it
			kit.MockProofs(),        // we don't care about proper sealing/proofs
			kit.SectorSize(512<<20), // 512MiB sectors
		)
		ensemble.InterconnectAll().BeginMining(50 * time.Millisecond)

		// set the *optional* on-chain multiaddr
		// the mind boggles: there is no API call for that - got to assemble your own msg
		{
			minfo, err := miner.FullNode.StateMinerInfo(ctx, miner.ActorAddr, lotustypes.EmptyTSK)
			if err != nil {
				return err
			}

			maddrNop2p, _ := multiaddr.SplitFunc(miner.ListenAddr, func(c multiaddr.Component) bool {
				return c.Protocol().Code == multiaddr.P_P2P
			})

			params, aerr := lotusactors.SerializeParams(&filminer.ChangeMultiaddrsParams{NewMultiaddrs: [][]byte{maddrNop2p.Bytes()}})
			if aerr != nil {
				return aerr
			}

			_, err = miner.FullNode.MpoolPushMessage(ctx, &lotustypes.Message{
				To:     miner.ActorAddr,
				From:   minfo.Worker,
				Value:  lotustypes.NewInt(0),
				Method: lotusminer.Methods.ChangeMultiaddrs,
				Params: params,
			}, nil)
			if err != nil {
				return err
			}
		}

		fmt.Printf("Test client fullnode running on %s\n", client.ListenAddr)
		os.Setenv("FULLNODE_API_INFO", client.ListenAddr.String())

		client.WaitTillChain(ctx, kit.BlockMinedBy(miner.ActorAddr))

		// FilClient initialization
		fmt.Printf("Initializing filclient...\n")

		// give filc the pre-funded wallet from the client
		ki, err := client.WalletExport(ctx, client.DefaultKey.Address)
		require.NoError(t, err)
		lr, err := lotusrepo.NewMemory(nil).Lock(lotusrepo.Wallet)
		require.NoError(t, err)
		ks, err := lr.KeyStore()
		require.NoError(t, err)
		wallet, err := wallet.NewWallet(ks)
		require.NoError(t, err)
		_, err = wallet.WalletImport(ctx, ki)
		require.NoError(t, err)

		h, err := ensemble.MN.GenPeer()
		if err != nil {
			t.Fatalf("Could not gen p2p peer: %v", err)
		}
		ensemble.MN.LinkAll()
		api, closer := initAPI(t, cctx)
		bs := initBlockstore(t)
		ds := initDatastore(t)
		defer closer()
		fc, err := NewClient(h, api, wallet, client.DefaultKey.Address, bs, ds, t.TempDir())
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

		// Tests
		fmt.Printf("Starting tests...\n")

		t.Run("storage", func(t *testing.T) {
			addr, err := miner.ActorAddress(ctx)
			fmt.Printf("Actor address: %s\n", addr)
			if err != nil {
				t.Fatalf("Could not get miner address: %s", err)
			}

			fmt.Printf("Testing storage deal for miner %s\n", addr)

			ask, err := fc.GetAsk(ctx, addr)
			if err != nil {
				t.Fatalf("Failed to get ask from miner: %v", err)
			}

			_, err = fc.LockMarketFunds(
				ctx,
				lotustypes.FIL(lotustypes.NewInt(1000000000000000)), // FIXME - no idea what's reasonable
			)
			require.NoError(t, err)

			proposal, err := fc.MakeDeal(ctx, addr, obj.Cid(), ask.Ask.Ask.Price, 0, 2880*365, false)
			if err != nil {
				t.Fatalf("Failed to make deal with miner: %v", err)
			}

			proposalRes, err := fc.SendProposal(ctx, proposal)
			if err != nil {
				t.Fatalf("Sending proposal failed: %v", err)
			}

			switch proposalRes.Response.State {
			case storagemarket.StorageDealWaitingForData, storagemarket.StorageDealProposalAccepted:
			default:
				t.Fatalf("Test deal was not accepted")
			}

			var chanid *datatransfer.ChannelID
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
				chanidCopy := *chanid
				chanidLk.Unlock()

				// Skip messages not related to this channel
				if state.ChannelID() != chanidCopy {
					return
				}

				switch event.Code {
				case datatransfer.CleanupComplete: // FIXME previously this was waiting for a code that would never come - not sure if CleanupComplete is right here....
					finish(nil)
				case datatransfer.Error:
					finish(fmt.Errorf("data transfer failed"))
				}
			})
			defer unsubscribe()

			chanidLk.Lock()
			chanid, err = fc.StartDataTransfer(ctx, miner.ActorAddr, proposalRes.Response.Proposal, obj.Cid())
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

			if err := fc.dataTransfer.CloseDataTransferChannel(ctx, *chanid); err != nil {
				t.Fatalf("Failed to close data transfer channel")
			}
		})

		t.Run("retrieval", func(t *testing.T) {
			// qres, err := fc.RetrievalQuery(ctx, miner.ActorAddr, obj.Cid())
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

func initAPI(t *testing.T, cctx *cli.Context) (api.Gateway, jsonrpc.ClientCloser) {
	api, closer, err := lcli.GetGatewayAPI(cctx)
	if err != nil {
		t.Fatalf("Could not initialize Lotus API gateway: %v", err)
	}

	return api, closer
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
