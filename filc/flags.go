package main

import cli "github.com/urfave/cli/v2"

var flagMiner = &cli.StringFlag{
	Name:    "miner",
	Aliases: []string{"miners", "m"},
}

var flagMinerRequired = &cli.StringFlag{
	Name:     flagMiner.Name,
	Aliases:  flagMiner.Aliases,
	Required: true,
}

var flagVerified = &cli.BoolFlag{
	Name: "verified",
}

var flagOutput = &cli.PathFlag{
	Name:    "output",
	Aliases: []string{"o"},
}
