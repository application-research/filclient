module github.com/application-research/filclient

go 1.16

require (
	github.com/dustin/go-humanize v1.0.0
	github.com/fatih/color v1.9.0 // indirect
	github.com/filecoin-project/go-address v0.0.6
	github.com/filecoin-project/go-bs-lmdb v1.0.5
	github.com/filecoin-project/go-cbor-util v0.0.1
	github.com/filecoin-project/go-commp-utils v0.1.3
	github.com/filecoin-project/go-data-transfer v1.11.7
	github.com/filecoin-project/go-fil-commcid v0.1.0
	github.com/filecoin-project/go-fil-commp-hashhash v0.1.0
	github.com/filecoin-project/go-fil-markets v1.13.3
	github.com/filecoin-project/go-paramfetch v0.0.2 // indirect
	github.com/filecoin-project/go-state-types v0.1.1-0.20210915140513-d354ccf10379
	github.com/filecoin-project/lotus v1.13.0
	github.com/filecoin-project/specs-actors/v6 v6.0.1
	github.com/gdamore/tcell/v2 v2.2.0 // indirect
	github.com/ipfs/go-bitswap v0.4.1-0.20211029155204-92d1e7aaf1dd
	github.com/ipfs/go-blockservice v0.1.7
	github.com/ipfs/go-cid v0.1.0
	github.com/ipfs/go-datastore v0.4.6
	github.com/ipfs/go-ds-leveldb v0.4.2
	github.com/ipfs/go-graphsync v0.10.9
	github.com/ipfs/go-ipfs-blockstore v1.0.5-0.20210802214209-c56038684c45
	github.com/ipfs/go-ipfs-chunker v0.0.5
	github.com/ipfs/go-ipfs-exchange-offline v0.0.1
	github.com/ipfs/go-ipfs-files v0.0.9
	github.com/ipfs/go-ipld-format v0.2.0
	github.com/ipfs/go-ipns v0.1.2 // indirect
	github.com/ipfs/go-log/v2 v2.3.0
	github.com/ipfs/go-merkledag v0.4.1
	github.com/ipfs/go-unixfs v0.2.6
	github.com/ipld/go-car v0.3.2-0.20211001225732-32d0d9933823
	github.com/ipld/go-codec-dagpb v1.3.0
	github.com/ipld/go-ipld-prime v0.12.3
	github.com/ipld/go-ipld-selector-text-lite v0.0.0
	github.com/kr/text v0.2.0 // indirect
	github.com/libp2p/go-libp2p v0.15.1
	github.com/libp2p/go-libp2p-core v0.9.0
	github.com/libp2p/go-libp2p-kad-dht v0.13.1
	github.com/libp2p/go-libp2p-protocol v0.1.0
	github.com/mitchellh/go-homedir v1.1.0
	github.com/multiformats/go-multiaddr v0.4.1
	github.com/urfave/cli/v2 v2.3.0
	github.com/whyrusleeping/base32 v0.0.0-20170828182744-c30ac30633cc
	go.opentelemetry.io/otel v1.2.0
	go.opentelemetry.io/otel/trace v1.2.0
	go.uber.org/zap v1.19.1
	golang.org/x/term v0.0.0-20210927222741-03fcf44c2211
	golang.org/x/xerrors v0.0.0-20200804184101-5ec99f83aff1
)

replace github.com/libp2p/go-libp2p-yamux => github.com/libp2p/go-libp2p-yamux v0.5.1

replace github.com/filecoin-project/filecoin-ffi => ./extern/filecoin-ffi
