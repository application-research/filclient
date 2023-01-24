package filclient

import (
	"fmt"
	"strconv"
	"strings"

	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/libp2p/go-libp2p/core/peer"
)

func ChannelIDFromString(id string) (*datatransfer.ChannelID, error) {
	if id == "" {
		return nil, fmt.Errorf("cannot parse empty string as channel id")
	}

	parts := strings.Split(id, "-")
	if len(parts) != 3 {
		return nil, fmt.Errorf("cannot parse channel id '%s': expected format 'initiator-responder-transferid'", id)
	}

	initiator, err := peer.Decode(parts[0])
	if err != nil {
		return nil, fmt.Errorf("parsing initiator peer id '%s' in channel id '%s'", parts[0], id)
	}

	responder, err := peer.Decode(parts[1])
	if err != nil {
		return nil, fmt.Errorf("parsing responder peer id '%s' in channel id '%s'", parts[1], id)
	}

	xferid, err := strconv.ParseUint(parts[2], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parsing transfer id '%s' in channel id '%s'", parts[2], id)
	}

	return &datatransfer.ChannelID{
		Initiator: initiator,
		Responder: responder,
		ID:        datatransfer.TransferID(xferid),
	}, nil
}
