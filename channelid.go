package filclient

import (
	"strconv"
	"strings"

	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/libp2p/go-libp2p-core/peer"
)

func ChannelIDFromString(id string) *datatransfer.ChannelID {
	if id == "" {
		return nil
	}

	parts := strings.Split(id, "-")
	if len(parts) != 3 {
		return nil
	}

	initiator, err := peer.Decode(parts[0])
	if err != nil {
		return nil
	}

	responder, err := peer.Decode(parts[1])
	if err != nil {
		return nil
	}

	xferid, err := strconv.ParseUint(parts[2], 10, 64)
	if err != nil {
		return nil
	}

	return &datatransfer.ChannelID{
		Initiator: initiator,
		Responder: responder,
		ID:        datatransfer.TransferID(xferid),
	}
}
