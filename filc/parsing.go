package main

import (
	"fmt"
	"strings"

	"github.com/filecoin-project/go-address"
	cli "github.com/urfave/cli/v2"
)

// Read a single miner from the CLI, erroring if more than one is specified, or
// none are present.
func readMiner(cctx *cli.Context) (address.Address, error) {
	miners, err := readMiners(cctx)
	if err != nil {
		return address.Undef, err
	}

	if len(miners) > 1 {
		return address.Undef, fmt.Errorf("only one miner expected")
	}

	return miners[0], nil
}

// Read a comma-separated list of miners from the CLI, erroring if none are
// present.
func readMiners(cctx *cli.Context) ([]address.Address, error) {
	minerStrings := strings.Split(cctx.String("miner"), ",")

	if len(minerStrings) == 0 {
		return nil, fmt.Errorf("no miners were specified")
	}

	miners := make([]address.Address, len(minerStrings))
	for i, ms := range minerStrings {
		miner, err := address.NewFromString(ms)
		if err != nil {
			return nil, fmt.Errorf("failed to parse miner %s: %w", ms, err)
		}

		miners[i] = miner
	}

	return miners, nil
}
