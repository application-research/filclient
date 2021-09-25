package main

import (
	"fmt"
	"os"

	logging "github.com/ipfs/go-log/v2"
	"github.com/mitchellh/go-homedir"
	cli "github.com/urfave/cli/v2"
	"go.uber.org/zap/zapcore"
)

var log = logging.Logger("filc")

func main() {
	//--system dt-impl --system dt-chanmon --system dt_graphsync --system graphsync --system data_transfer_network debug
	logging.SetPrimaryCore(zapcore.NewCore(zapcore.NewConsoleEncoder(zapcore.EncoderConfig{
		MessageKey: "message",
		TimeKey:    "time",
		LevelKey:   "level",

		EncodeLevel: zapcore.CapitalColorLevelEncoder,
		EncodeTime:  zapcore.TimeEncoderOfLayout("15:04:05"),

		ConsoleSeparator: " | ",
	}), os.Stdout, zapcore.InfoLevel))
	logging.SetLogLevel("filc", "info")
	// logging.SetLogLevel("dt-impl", "debug")
	// logging.SetLogLevel("dt-chanmon", "debug")
	// logging.SetLogLevel("dt_graphsync", "debug")
	// logging.SetLogLevel("data_transfer_network", "debug")
	// logging.SetLogLevel("filclient", "debug")
	app := cli.NewApp()

	app.Commands = []*cli.Command{
		makeDealCmd,
		getAskCmd,
		infoCmd,
		listDealsCmd,
		retrieveFileCmd,
		queryRetrievalCmd,
		clearBlockstoreCmd,
	}
	app.Flags = []cli.Flag{
		&cli.StringFlag{
			Name:  "repo",
			Value: "~/.lotus",
		},
	}

	// Store config dir in metadata
	ddir, err := homedir.Expand("~/.filc")
	if err != nil {
		fmt.Println("could not set config dir: ", err)
	}
	app.Metadata = map[string]interface{}{
		"ddir": ddir,
	}

	// ...and make sure the directory exists
	if err := os.MkdirAll(ddir, 0755); err != nil {
		fmt.Println("could not create config directory: ", err)
		os.Exit(1)
	}

	if err := app.Run(os.Args); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

// Get config directory from CLI metadata.
func ddir(cctx *cli.Context) string {
	mDdir := cctx.App.Metadata["ddir"]
	switch ddir := mDdir.(type) {
	case string:
		return ddir
	default:
		panic("ddir should be present in CLI metadata")
	}
}
