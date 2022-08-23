package main

import (
	"fmt"
	"os"

	logging "github.com/ipfs/go-log/v2"
	"github.com/mitchellh/go-homedir"
	cli "github.com/urfave/cli/v2"
	"go.uber.org/zap/zapcore"
)

func init() {
	if os.Getenv("FULLNODE_API_INFO") == "" {
		os.Setenv("FULLNODE_API_INFO", "wss://api.chain.love")
	}
}

var log = logging.Logger("filctl")

func main() {
	logging.SetPrimaryCore(zapcore.NewCore(zapcore.NewConsoleEncoder(zapcore.EncoderConfig{
		MessageKey: "message",
		TimeKey:    "time",
		LevelKey:   "level",

		EncodeLevel: zapcore.CapitalColorLevelEncoder,
		EncodeTime:  zapcore.TimeEncoderOfLayout("15:04:05"),

		ConsoleSeparator: "  ",
	}), os.Stdout, zapcore.DebugLevel))

	logging.SetLogLevel("filctl", "info")

	defer log.Sync()

	app := cli.NewApp()

	app.Commands = []*cli.Command{
		printLoggersCmd,
		makeDealCmd,
		dealStatusCmd,
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
