module github.com/application-research/filclient

go 1.16

require (
	contrib.go.opencensus.io/exporter/prometheus v0.2.0 // indirect
	github.com/DataDog/zstd v1.4.4 // indirect
	github.com/OneOfOne/xxhash v1.2.5 // indirect
	github.com/dgryski/go-farm v0.0.0-20200201041132-a6ae2369ad13 // indirect
	github.com/dustin/go-humanize v1.0.0
	github.com/filecoin-project/go-address v0.0.6
	github.com/filecoin-project/go-bs-lmdb v1.0.5
	github.com/filecoin-project/go-cbor-util v0.0.0-20191219014500-08c40a1e63a2
	github.com/filecoin-project/go-commp-utils v0.1.2
	github.com/filecoin-project/go-data-transfer v1.11.2
	github.com/filecoin-project/go-fil-commcid v0.1.0
	github.com/filecoin-project/go-fil-commp-hashhash v0.1.0
	github.com/filecoin-project/go-fil-markets v1.12.0
	github.com/filecoin-project/go-state-types v0.1.1-0.20210810190654-139e0e79e69e
	github.com/filecoin-project/lotus v1.11.3-rc1
	github.com/filecoin-project/specs-actors v0.9.14
	github.com/go-ole/go-ole v1.2.5 // indirect
	github.com/golang/snappy v0.0.2 // indirect
	github.com/gorilla/mux v1.8.0 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	github.com/ipfs/go-bitswap v0.3.4
	github.com/ipfs/go-blockservice v0.1.7
	github.com/ipfs/go-cid v0.1.0
	github.com/ipfs/go-datastore v0.4.6
	github.com/ipfs/go-ds-leveldb v0.4.2
	github.com/ipfs/go-graphsync v0.10.0
	github.com/ipfs/go-ipfs-blockstore v1.0.5-0.20210802214209-c56038684c45
	github.com/ipfs/go-ipfs-chunker v0.0.5
	github.com/ipfs/go-ipfs-exchange-offline v0.0.1
	github.com/ipfs/go-ipfs-files v0.0.8
	github.com/ipfs/go-ipld-format v0.2.0
	github.com/ipfs/go-log v1.0.5
	github.com/ipfs/go-merkledag v0.4.0
	github.com/ipfs/go-unixfs v0.2.6
	github.com/ipld/go-car v0.3.1-0.20210601190600-f512dac51e8e
	github.com/ipld/go-codec-dagpb v1.3.0
	github.com/ipld/go-ipld-prime v0.12.3
	github.com/ipld/go-ipld-selector-text-lite v0.0.0-20210817134355-4c190a2bb825
	github.com/kr/text v0.2.0 // indirect
	github.com/libp2p/go-libp2p v0.14.2
	github.com/libp2p/go-libp2p-core v0.8.6
	github.com/libp2p/go-libp2p-crypto v0.1.0
	github.com/libp2p/go-libp2p-protocol v0.1.0
	github.com/mitchellh/go-homedir v1.1.0
	github.com/multiformats/go-multiaddr v0.3.3
	github.com/rs/cors v1.7.0 // indirect
	github.com/shirou/gopsutil v3.20.12+incompatible // indirect
	github.com/urfave/cli/v2 v2.3.0
	github.com/valyala/fasttemplate v1.2.1 // indirect
	github.com/whyrusleeping/base32 v0.0.0-20170828182744-c30ac30633cc
	go.opentelemetry.io/otel v0.20.0
	go.opentelemetry.io/otel/trace v0.20.0
	golang.org/x/xerrors v0.0.0-20200804184101-5ec99f83aff1
	google.golang.org/genproto v0.0.0-20210122163508-8081c04a3579 // indirect
	google.golang.org/grpc v1.36.0 // indirect
)

replace github.com/libp2p/go-libp2p-yamux => github.com/libp2p/go-libp2p-yamux v0.5.1

replace github.com/filecoin-project/filecoin-ffi => ./extern/filecoin-ffi
