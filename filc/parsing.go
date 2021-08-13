package main

import (
	"fmt"
	"io/fs"
	"strings"

	"github.com/filecoin-project/go-address"
	cli "github.com/urfave/cli/v2"
)

// Read a single miner from the CLI, erroring if more than one is specified, or
// none are present.
func parseMiner(cctx *cli.Context) (address.Address, error) {
	miners, err := parseMiners(cctx)
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
func parseMiners(cctx *cli.Context) ([]address.Address, error) {
	// Each minerStringsRaw element may contain multiple comma-separated values
	minerStringsRaw := cctx.StringSlice(flagMiner.Name)
	if len(minerStringsRaw) == 0 {
		return nil, fmt.Errorf("no miners were specified")
	}

	// Split any comma-separated minerStringsRaw elements
	var minerStrings []string
	for _, raw := range minerStringsRaw {
		minerStrings = append(minerStrings, strings.Split(raw, ",")...)
	}

	var miners []address.Address
	for _, ms := range minerStrings {

		miner, err := address.NewFromString(ms)
		if err != nil {
			return nil, fmt.Errorf("failed to parse miner %s: %w", ms, err)
		}

		miners = append(miners, miner)
	}

	return miners, nil
}

// Get whether to use a verified deal or not.
func parseVerified(cctx *cli.Context) bool {
	return cctx.Bool(flagVerified.Name)
}

// Get the destination file to write the output to, erroring if not a valid
// path. This early error check is important because you don't want to do a
// bunch of work, only to end up crashing when you try to write the file.
func parseOutput(cctx *cli.Context) (string, error) {
	path := cctx.String(flagOutput.Name)

	if !fs.ValidPath(path) {
		return "", fmt.Errorf("invalid output location '%s'", path)
	}

	return path, nil
}