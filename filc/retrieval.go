package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"sync"
	"time"

	"github.com/application-research/filclient"
	"github.com/application-research/filclient/retrievehelper"
	"github.com/dustin/go-humanize"
	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-fil-markets/retrievalmarket"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/ipfs/go-blockservice"
	"github.com/ipfs/go-cid"
	ipldformat "github.com/ipfs/go-ipld-format"
	"github.com/ipfs/go-merkledag"
	"github.com/ipld/go-ipld-prime"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"golang.org/x/xerrors"
)

type RetrievalCandidate struct {
	Miner   address.Address
	RootCid cid.Cid
	DealID  uint
}

type CandidateSelectionConfig struct {
	// Whether retrieval over IPFS is preferred if available
	tryIPFS bool

	// If true, candidates will be tried in the order they're passed in
	// unchanged (and all other sorting-related options will be ignored)
	noSort bool
}

type RetrievalResults struct {
}

func (node *Node) GetRetrievalCandidates(endpoint string, c cid.Cid) ([]RetrievalCandidate, error) {

	endpointURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, xerrors.Errorf("endpoint %s is not a valid url", endpoint)
	}
	endpointURL.Path = path.Join(endpointURL.Path, c.String())

	resp, err := http.Get(endpointURL.String())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http request to endpoint %s got status %v", endpointURL, resp.StatusCode)
	}

	var res []RetrievalCandidate

	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, xerrors.Errorf("could not unmarshal http response for cid %s", c)
	}

	return res, nil
}

type RetrievalStats interface {
	GetByteSize() uint64
	GetDuration() time.Duration
	GetAverageBytesPerSecond() uint64
}

type FILRetrievalStats struct {
	filclient.RetrievalStats
}

func (stats *FILRetrievalStats) GetByteSize() uint64 {
	return stats.Size
}

func (stats *FILRetrievalStats) GetDuration() time.Duration {
	return stats.Duration
}

func (stats *FILRetrievalStats) GetAverageBytesPerSecond() uint64 {
	return stats.AverageSpeed
}

type IPFSRetrievalStats struct {
	ByteSize uint64
	Duration time.Duration
}

func (stats *IPFSRetrievalStats) GetByteSize() uint64 {
	return stats.ByteSize
}

func (stats *IPFSRetrievalStats) GetDuration() time.Duration {
	return stats.Duration
}

func (stats *IPFSRetrievalStats) GetAverageBytesPerSecond() uint64 {
	return uint64(float64(stats.ByteSize) / stats.Duration.Seconds())
}

// NOTE (6 Oct 2021) - presently, if selNode is specified, then cfg.tryIPFS will
// be silently ignored, and an ipfs retrieval will not be attempted. This will
// be fixed very soon.
func (node *Node) RetrieveFromBestCandidate(
	ctx context.Context,
	fc *filclient.FilClient,
	c cid.Cid,
	selNode ipld.Node,
	candidates []RetrievalCandidate,
	cfg CandidateSelectionConfig,
) (RetrievalStats, error) {
	// Try IPFS first, if requested
	//
	// TODO (6 Oct 2021): we should only check cfg.tryIPFS here in the future
	// (see function comment)
	if cfg.tryIPFS && selNode != nil {
		log.Info("Searching IPFS for CID...")

		log.Info("Bootstrapping DHT...")
		bootstrapPeers := dht.GetDefaultBootstrapPeerAddrInfos()

		dht, err := dht.New(
			ctx,
			node.Host,
			dht.Mode(dht.ModeClient),
		)
		if err != nil {
			// TODO: don't die here
			return nil, err
		}

		// Connect to IPFS peers in parallel

		var peersConnected sync.WaitGroup
		var peersLk sync.Mutex
		peersConnectedCount := 0

		peersConnected.Add(len(bootstrapPeers))

		for _, ai := range bootstrapPeers {
			ai := ai

			go func() {
				defer func() {
					peersConnected.Done()
					peersLk.Lock()
					peersConnectedCount++
					fmt.Printf("%v/%v\r", peersConnectedCount, len(bootstrapPeers))
					peersLk.Unlock()
				}()

				if err := node.Host.Connect(ctx, ai); err != nil {
					log.Errorf("Couldn't connect to bootstrap peer %s", ai)
					return
				}
			}()
		}
		peersConnected.Wait()

		if err := dht.Bootstrap(ctx); err != nil {
			return nil, err
		}

		log.Infof("Finished bootstrapping, getting providers...")

		providers, err := dht.FindProviders(ctx, c)
		if err != nil {
			// TODO: don't die here
			return nil, err
		}

		avail := len(providers) > 0

		if avail {
			log.Info("The CID was found on IPFS, connecting to providers...")

			// Connect to the retrieved providers
			connectedCount := 0
			for _, provider := range providers {
				if err := node.Host.Connect(ctx, provider); err != nil {
					log.Debugf("Failed to connect to IPFS provider %s: %v", provider, err)
					continue
				}
				connectedCount++
			}

			// If we were able to connect to at least one of the providers, go ahead
			// with the retrieval

			var progressLk sync.Mutex
			var bytesRetrieved uint64 = 0
			startTime := time.Now()

			if connectedCount > 0 {
				log.Infof("Successfully connected to %v/%v providers", connectedCount, len(providers))

				log.Info("Starting IPFS retrieval")

				bserv := blockservice.New(node.Blockstore, node.Bitswap)
				dserv := merkledag.NewDAGService(bserv)
				//dsess := dserv.Session(ctx)

				cset := cid.NewSet()
				if err := merkledag.Walk(ctx, func(ctx context.Context, c cid.Cid) ([]*ipldformat.Link, error) {
					node, err := dserv.Get(ctx, c)
					if err != nil {
						return nil, err
					}

					progressLk.Lock()
					nodeSize, err := node.Size()
					if err != nil {
						nodeSize = 0
					}
					bytesRetrieved += nodeSize
					fmt.Printf("%v (%v)\r", bytesRetrieved, humanize.IBytes(bytesRetrieved))
					progressLk.Unlock()

					if c.Type() == cid.Raw {
						return nil, nil
					}

					return node.Links(), nil
				}, c, cset.Visit, merkledag.Concurrent()); err != nil {
					return nil, err
				}

				log.Info("IPFS retrieval succeeded")

				return &IPFSRetrievalStats{
					ByteSize: bytesRetrieved,
					Duration: time.Since(startTime),
				}, nil
			} else {
				log.Info("Could not connect to any providers, will not attempt IPFS retrieval")
			}
		} else {
			log.Info("Could not find the CID on IPFS")
		}
	}

	// If no miners are provided, there's nothing else we can do
	if len(candidates) == 0 {
		log.Info("No miners were provided, will not attempt FIL retrieval")
		return nil, xerrors.Errorf("retrieval failed: no miners were provided")
	}

	// If IPFS retrieval was unavailable, do a full FIL retrieval. Start with
	// querying all the candidates for sorting.

	log.Info("Querying FIL retrieval candidates...")

	type CandidateQuery struct {
		Candidate RetrievalCandidate
		Response  *retrievalmarket.QueryResponse
	}
	checked := 0
	var queries []CandidateQuery
	var queriesLk sync.Mutex

	var wg sync.WaitGroup
	wg.Add(len(candidates))

	for _, candidate := range candidates {

		// Copy into loop, cursed go
		candidate := candidate

		go func() {
			defer wg.Done()

			query, err := fc.RetrievalQuery(ctx, candidate.Miner, candidate.RootCid)
			if err != nil {
				log.Debugf("Retrieval query for miner %s failed: %v", candidate.Miner, err)
				return
			}

			queriesLk.Lock()
			queries = append(queries, CandidateQuery{Candidate: candidate, Response: query})
			checked++
			fmt.Printf("%v/%v\r", checked, len(candidates))
			queriesLk.Unlock()
		}()
	}

	wg.Wait()

	log.Infof("Got back %v retrieval query results of a total of %v candidates", len(queries), len(candidates))

	if len(queries) == 0 {
		return nil, xerrors.Errorf("retrieval failed: queries failed for all miners")
	}

	// After we got the query results, sort them with respect to the candidate
	// selection config as long as noSort isn't requested (TODO - more options)

	if !cfg.noSort {
		sort.Slice(queries, func(i, j int) bool {
			a := queries[i].Response
			b := queries[i].Response

			// Always prefer unsealed to sealed, no matter what
			if a.UnsealPrice.IsZero() && !b.UnsealPrice.IsZero() {
				return true
			}

			// Select lower price, or continue if equal
			aTotalPrice := totalCost(a)
			bTotalPrice := totalCost(b)
			if !aTotalPrice.Equals(bTotalPrice) {
				return aTotalPrice.LessThan(bTotalPrice)
			}

			// Select smaller size, or continue if equal
			if a.Size != b.Size {
				return a.Size < b.Size
			}

			return false
		})
	}

	// Now attempt retrievals in serial from first to last, until one works.
	// stats will get set if a retrieval succeeds - if no retrievals work, it
	// will still be nil after the loop finishes
	var stats RetrievalStats = nil
	for _, query := range queries {
		log.Infof("Attempting FIL retrieval with miner %s from root CID %s (%s FIL)", query.Candidate.Miner, query.Candidate.RootCid, totalCost(query.Response))
		log.Infof("Using selector %s")

		proposal, err := retrievehelper.RetrievalProposalForAsk(query.Response, query.Candidate.RootCid, selNode)
		if err != nil {
			log.Debugf("Failed to create retrieval proposal with candidate miner %s: %v", query.Candidate.Miner, err)
			continue
		}

		stats_, err := fc.RetrieveContent(ctx, query.Candidate.Miner, proposal)
		if err != nil {
			log.Debugf("Failed to retrieve content with candidate miner %s: %v", query.Candidate.Miner, err)
			continue
		}

		stats = &FILRetrievalStats{RetrievalStats: *stats_}
		break
	}

	if stats == nil {
		return nil, xerrors.New("retrieval failed: all miners failed to respond")
	}

	log.Info("FIL retrieval succeeded")

	return stats, nil
}

func (node *Node) RetrieveFromMiner(
	ctx context.Context,
	fc filclient.FilClient,
	miner address.Address,
	c cid.Cid,
) (*filclient.RetrievalStats, error) {
	ask, err := fc.RetrievalQuery(ctx, miner, c)
	if err != nil {
		return nil, err
	}

	proposal, err := retrievehelper.RetrievalProposalForAsk(ask, c, nil)
	if err != nil {
		return nil, err
	}

	stats, err := fc.RetrieveContent(ctx, miner, proposal)
	if err != nil {
		return nil, err
	}

	return stats, nil
}

func (node *Node) RetrieveFromIPFS(ctx context.Context, c cid.Cid) error {
	bserv := blockservice.New(node.Blockstore, node.Bitswap)
	dserv := merkledag.NewDAGService(bserv)

	cset := cid.NewSet()
	if err := merkledag.Walk(ctx, func(ctx context.Context, c cid.Cid) ([]*ipldformat.Link, error) {
		if c.Type() == cid.Raw {
			return nil, nil
		}

		node, err := dserv.Get(ctx, c)
		if err != nil {
			return nil, err
		}

		return node.Links(), nil
	}, c, cset.Visit, merkledag.Concurrent()); err != nil {
		return err
	}

	return nil
}

func totalCost(qres *retrievalmarket.QueryResponse) big.Int {
	return big.Add(big.Mul(qres.MinPricePerByte, big.NewIntUnsigned(qres.Size)), qres.UnsealPrice)
}
