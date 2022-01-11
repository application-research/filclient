package filclient

import (
	"path/filepath"
	"testing"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-jsonrpc"
	"github.com/filecoin-project/lotus/api"
	"github.com/filecoin-project/lotus/cli"
	"github.com/ipfs/go-datastore"
	flatfs "github.com/ipfs/go-ds-flatfs"
	leveldb "github.com/ipfs/go-ds-leveldb"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/host"
)

func TestMain(t *testing.T) {

	h := initHost(t)
	api, closer := initAPI(t)
	addr := initAddress(t)
	bs := initBlockstore(t)
	ds := initDatastore(t)
	defer closer()

	// FilClient initialization

	_, err := NewClient(h, api, nil, addr, bs, ds, t.TempDir())
	if err != nil {
		t.Fatalf("Could not initialize FilClient: %v", err)
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

func initAPI(t *testing.T) (api.Gateway, jsonrpc.ClientCloser) {
	api, closer, err := cli.GetGatewayAPI(nil)
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
