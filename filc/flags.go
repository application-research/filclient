package main

import cli "github.com/urfave/cli/v2"

var flagMiner = &cli.StringFlag{
	Name:    "miner",
	Aliases: []string{"m"},
}

var flagMinerRequired = &cli.StringFlag{
	Name:     flagMiner.Name,
	Aliases:  flagMiner.Aliases,
	Required: true,
}

var flagMiners = &cli.StringSliceFlag{
	Name:    "miners",
	Aliases: []string{"miner", "m"},
}

var flagMinersRequired = &cli.StringSliceFlag{
	Name:     flagMiners.Name,
	Aliases:  flagMiners.Aliases,
	Required: true,
}

var flagVerified = &cli.BoolFlag{
	Name: "verified",
}

var flagOutput = &cli.StringFlag{
	Name:    "output",
	Aliases: []string{"o"},
}

var flagNoIPFS = &cli.BoolFlag{
	Name: "no-ipfs",
}
