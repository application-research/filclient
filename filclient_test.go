package filclient

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	cborutil "github.com/filecoin-project/go-cbor-util"
	"github.com/filecoin-project/go-commp-utils/writer"
	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/filecoin-project/go-jsonrpc"
	"github.com/filecoin-project/lotus/api"
	lotusactors "github.com/filecoin-project/lotus/chain/actors"
	lotustypes "github.com/filecoin-project/lotus/chain/types"
	"github.com/filecoin-project/lotus/chain/wallet"
	lcli "github.com/filecoin-project/lotus/cli"
	"github.com/filecoin-project/lotus/itests/kit"
	lotusrepo "github.com/filecoin-project/lotus/node/repo"
	filbuiltin "github.com/filecoin-project/specs-actors/v6/actors/builtin"
	filminer "github.com/filecoin-project/specs-actors/v6/actors/builtin/miner"
	"github.com/ipfs/go-blockservice"
	"github.com/ipfs/go-datastore"
	flatfs "github.com/ipfs/go-ds-flatfs"
	leveldb "github.com/ipfs/go-ds-leveldb"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	chunk "github.com/ipfs/go-ipfs-chunker"
	"github.com/ipfs/go-merkledag"
	"github.com/ipfs/go-unixfs/importer"
	car "github.com/ipld/go-car"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"
	"golang.org/x/xerrors"

	carv2 "github.com/ipld/go-car/v2"
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

// func TestRetrieval(t *testing.T) {
// 	app := cli.NewApp()
// 	app.Action = func(cctx *cli.Context) error {
// 		client, miner, ensemble, fc, closer := initEnsemble(t, cctx)
// 		defer closer()

// 		// Create dummy deal on miner
// 		res, file := client.CreateImportFile(cctx.Context, 1, 256<<20)
// 		pieceInfo, err := client.ClientDealPieceCID(cctx.Context, res.Root)
// 		require.NoError(t, err)
// 		dh := kit.NewDealHarness(t, client, miner, miner)
// 		dp := dh.DefaultStartDealParams()
// 		dp.DealStartEpoch = abi.ChainEpoch(4 << 10)
// 		dp.Data = &storagemarket.DataRef{
// 			TransferType: storagemarket.TTManual,
// 			Root:         res.Root,
// 			PieceCid:     &pieceInfo.PieceCID,
// 			PieceSize:    pieceInfo.PieceSize.Unpadded(),
// 		}
// 		proposalCid := dh.StartDeal(cctx.Context, dp)
// 		require.Eventually(t, func() bool {
// 			cd, _ := client.ClientGetDealInfo(cctx.Context, *proposalCid)
// 			return cd.State == storagemarket.StorageDealCheckForAcceptance
// 		}, 30*time.Second, 1*time.Second)

// 		carFileDir := t.TempDir()
// 		carFilePath := filepath.Join(carFileDir, "out.car")
// 		require.NoError(t, client.ClientGenCar(cctx.Context, api.FileRef{Path: file}, carFilePath))
// 		require.NoError(t, miner.DealsImportData(cctx.Context, *proposalCid, carFilePath))

// 		query, err := fc.RetrievalQuery(cctx.Context, miner.ActorAddr, res.Root)
// 		err := fc.RetrieveContent(cctx.Context, miner.ActorAddr, &retrievalmarket.DealProposal{
// 			PayloadCID: res.Root,
// 			ID: query.,
// 		})

// 		return nil
// 	}
// 	if err := app.Run([]string{""}); err != nil {
// 		t.Fatalf("App failed: %v", err)
// 	}
// }

func TestStorage(t *testing.T) {
	app := cli.NewApp()
	app.Action = func(cctx *cli.Context) error {
		_, miner, _, fc, closer := initEnsemble(t, cctx)
		defer closer()

		ctx := cctx.Context

		bserv := blockservice.New(fc.blockstore, nil)
		dserv := merkledag.NewDAGService(bserv)

		spl := chunk.DefaultSplitter(NewDummyDataGen(128 << 20))

		obj, err := importer.BuildDagFromReader(dserv, spl)
		if err != nil {
			t.Fatalf("Could not build test data DAG: %v", err)
		}

		version, err := fc.GetMinerVersion(cctx.Context, miner.ActorAddr)
		require.NoError(t, err)
		fmt.Printf("Miner Version: %s\n", version)

		addr, err := miner.ActorAddress(ctx)
		require.NoError(t, err)

		fmt.Printf("Testing storage deal for miner %s\n", addr)

		ask, err := fc.GetAsk(ctx, addr)
		require.NoError(t, err)

		_, err = fc.LockMarketFunds(
			ctx,
			lotustypes.FIL(lotustypes.NewInt(1000000000000000)), // FIXME - no idea what's reasonable
		)
		require.NoError(t, err)

		proposal, err := fc.MakeDeal(ctx, addr, obj.Cid(), ask.Ask.Ask.Price, 0, 2880*365, false)
		require.NoError(t, err)

		fmt.Printf("Sending proposal\n")

		propnd, err := cborutil.AsIpld(proposal.DealProposal)
		if err != nil {
			return xerrors.Errorf("failed to compute deal proposal ipld node: %w", err)
		}

		propCid := propnd.Cid()

		_, err = fc.SendProposalV110(ctx, *proposal, propCid)
		require.NoError(t, err)

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
			default:
				fmt.Printf("Other event code \"%s\" (%v)", datatransfer.Events[event.Code], event.Code)
			}
		})
		defer unsubscribe()

		fmt.Printf("Starting data transfer\n")

		chanidLk.Lock()
		chanid, err = fc.StartDataTransfer(ctx, miner.ActorAddr, propCid, obj.Cid())
		chanidLk.Unlock()
		require.NoError(t, err)

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

		fmt.Printf("Data transfer finished\n")

		// TODO: bad position for testing retrieval query to peer
		query, err := fc.RetrievalQueryToPeer(ctx, peer.AddrInfo{ID: miner.Libp2p.PeerID, Addrs: []multiaddr.Multiaddr{miner.ListenAddr}}, obj.Cid())
		require.NoError(t, err)

		fmt.Printf("query: %#v\n", query)

		return nil
	}
	if err := app.Run([]string{""}); err != nil {
		t.Fatalf("App failed: %v", err)
	}
}

// -- Setup functions

// Create and set up an ensemble with linked filclient
func initEnsemble(t *testing.T, cctx *cli.Context) (*kit.TestFullNode, *kit.TestMiner, *kit.Ensemble, *FilClient, func()) {

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
		minfo, err := miner.FullNode.StateMinerInfo(cctx.Context, miner.ActorAddr, lotustypes.EmptyTSK)
		require.NoError(t, err)

		maddrNop2p, _ := multiaddr.SplitFunc(miner.ListenAddr, func(c multiaddr.Component) bool {
			return c.Protocol().Code == multiaddr.P_P2P
		})

		params, aerr := lotusactors.SerializeParams(&filminer.ChangeMultiaddrsParams{NewMultiaddrs: [][]byte{maddrNop2p.Bytes()}})
		require.NoError(t, aerr)

		_, err = miner.FullNode.MpoolPushMessage(cctx.Context, &lotustypes.Message{
			To:     miner.ActorAddr,
			From:   minfo.Worker,
			Value:  lotustypes.NewInt(0),
			Method: filbuiltin.MethodsMiner.ChangeMultiaddrs,
			Params: params,
		}, nil)
		require.NoError(t, err)
	}

	fmt.Printf("Test client fullnode running on %s\n", client.ListenAddr)
	os.Setenv("FULLNODE_API_INFO", client.ListenAddr.String())

	client.WaitTillChain(cctx.Context, kit.BlockMinedBy(miner.ActorAddr))

	// FilClient initialization
	fmt.Printf("Initializing filclient...\n")

	// give filc the pre-funded wallet from the client
	ki, err := client.WalletExport(cctx.Context, client.DefaultKey.Address)
	require.NoError(t, err)
	lr, err := lotusrepo.NewMemory(nil).Lock(lotusrepo.Wallet)
	require.NoError(t, err)
	ks, err := lr.KeyStore()
	require.NoError(t, err)
	wallet, err := wallet.NewWallet(ks)
	require.NoError(t, err)
	_, err = wallet.WalletImport(cctx.Context, ki)
	require.NoError(t, err)

	h, err := ensemble.Mocknet().GenPeer()
	if err != nil {
		t.Fatalf("Could not gen p2p peer: %v", err)
	}
	ensemble.Mocknet().LinkAll()
	api, closer := initAPI(t, cctx)
	bs := initBlockstore(t)
	ds := initDatastore(t)
	fc, err := NewClient(h, api, wallet, client.DefaultKey.Address, bs, ds, t.TempDir())
	if err != nil {
		t.Fatalf("Could not initialize FilClient: %v", err)
	}

	time.Sleep(time.Millisecond * 500)

	return client, miner, ensemble, fc, closer
}

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

func TestGetDealStatus(t *testing.T) {

}

func TestGeneratePieceCommitmentFFI(t *testing.T) {
	bs := initBlockstore(t)
	//ds := initDatastore(t)

	/*
		2022-07-29T15:59:53.798Z	ERROR	estuary	estuary/replication.go:1606	deal state for deal 651bfbd1-cd87-4639-b02b-f7ebe0431848
		from miner f02576 is error:
		failed to verify CommP: commP mismatch, expected=baga6ea4seaqbcseenju34x57zfdjhs3qvrgvl5qltd5w7yt4srur73s5jduikpy, actual=baga6ea4seaqashs3x4fqfddnok4gcri23jfhkjn45ox5uer2z4ddwhuruknmigq	{"app_version": "v0.1.7-dirty"}
	*/

	fi, err := os.Open("/Users/jay/Documents/car-files/1e347bf0-ce28-47a2-bf15-59472f4f87cf.download")
	if err != nil {
		t.Error(err)
	}

	bserv := blockservice.New(bs, nil)
	dserv := merkledag.NewDAGService(bserv)

	t.Log("importing file...")
	spl := chunk.DefaultSplitter(fi)

	obj, err := importer.BuildDagFromReader(dserv, spl)
	if err != nil {
		t.Error(err)
	}

	t.Logf("File CID: %s", obj.Cid())

	cid, preparedCarSize, unpaddedPieceSize, err := GeneratePieceCommitmentFFI(context.Background(), obj.Cid(), bs)
	if err != nil {
		t.Error(err)
	}
	t.Logf("%s, %d, %d", cid, preparedCarSize, unpaddedPieceSize)
	t.Log("Done.")
}

func TestGeneratePieceCommitmentFFI_2(t *testing.T) {
	ctx := context.Background()
	bs := initBlockstore(t)

	fi, err := os.Open("/Users/jay/Documents/car-files/1e347bf0-ce28-47a2-bf15-59472f4f87cf.download")
	if err != nil {
		t.Error(err)
	}

	header, err := car.LoadCar(ctx, bs, fi)
	if err != nil {
		t.Error(err)
	}

	if len(header.Roots) != 1 {
		// if someone wants this feature, let me know
		t.Error("cannot handle uploading car files with multiple roots")
	}
	rootCID := header.Roots[0]

	cid, preparedCarSize, unpaddedPieceSize, err := GeneratePieceCommitmentFFI(context.Background(), rootCID, bs)
	if err != nil {
		t.Error(err)
	}
	t.Logf("%s, %d, %d", cid, preparedCarSize, unpaddedPieceSize)
	t.Log("Done.")
}

func GenerateCommP(filepath string) (cidAndSize *writer.DataCIDSize, finalErr error) {
	rd, err := carv2.OpenReader(filepath)
	if err != nil {
		return nil, fmt.Errorf("failed to get CARv2 reader: %w", err)
	}

	defer func() {
		if err := rd.Close(); err != nil {
			if finalErr == nil {
				cidAndSize = nil
				finalErr = fmt.Errorf("failed to close CARv2 reader: %w", err)
				return
			}
		}
	}()

	// dump the CARv1 payload of the CARv2 file to the Commp Writer and get back the CommP.
	w := &writer.Writer{}
	r := rd.DataReader()

	written, err := io.Copy(w, r)
	if err != nil {
		return nil, fmt.Errorf("failed to write to CommP writer: %w", err)
	}

	var size int64
	switch rd.Version {
	case 2:
		size = int64(rd.Header.DataSize)
	case 1:
		st, err := os.Stat(filepath)
		if err != nil {
			return nil, fmt.Errorf("failed to stat CARv1 file: %w", err)
		}
		size = st.Size()
	}

	if written != size {
		return nil, fmt.Errorf("number of bytes written to CommP writer %d not equal to the CARv1 payload size %d", written, rd.Header.DataSize)
	}

	cidAndSize = &writer.DataCIDSize{}
	*cidAndSize, err = w.Sum()
	if err != nil {
		return nil, fmt.Errorf("failed to get CommP: %w", err)
	}

	return cidAndSize, nil
}

func TestGeneratePieceCommitmentFFI_3(t *testing.T) {

	cidAndSize, err := GenerateCommP("/Users/jay/Documents/car-files/1e347bf0-ce28-47a2-bf15-59472f4f87cf.download")
	if err != nil {
		t.Error(err)
	}
	t.Logf("%s", cidAndSize.PieceCID)
	t.Logf("%s, %d, %d", cidAndSize.PieceCID, cidAndSize.PieceSize, cidAndSize.PieceSize.Unpadded())
}
