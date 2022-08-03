# filclient

A standalone client library that enables users to interact with the Filecoin storage network. The types of interactions available are listed below, under "Features". 

This functionality is accomplished by allowing users to interact with the API of any Lotus Node on the Filecoin storage network. One such node is `chain.love`, which is a node hosted by the Outercore Engineering team. 

## Features

- Make storage deals with miners
  - Query storage ask prices
  - Construct and sign deal proposals
  - Send deal proposals over the network to miners
  - Send data to miners via data transfer and graphsync
  - Check status of a deal with a miner
- Make retrieval deals with miners
  - Query retrieval ask prices
  - Run retrieval protocol, get data
- Data transfer management
  - Ability to query data transfer (in or out) status
- Market funds management
  - Check and add to market escrow balance
- Local message signing capabilities and wallet management

## Roadmap

- [ ] Cleanup and organization of functions
- [ ] Godocs on main methods
- [ ] Direct mempool integration (to avoid relying on a lotus gateway node)
- [ ] Cleanup of dependency tree
  - [ ] Remove dependency on filecoin-ffi
  - [ ] Remove dependency on go-fil-markets
  - [ ] Remove dependency on main lotus repo
- [ ] Good usage examples
  - [ ] Sample application using filclient
- [ ] Integration testing

## License

MIT
