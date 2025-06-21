# CPU Miner

A very simple (and naive) single-threaded CPU miner for Siacoin testnet mining.

## Prerequisites

- A running walletd node. 
  - Does not need to have any addresses or wallets.
- A Siacoin address to mine to

## Building
```sh
go build -o bin/ ./cmd/cpuminerd
```

## Usage

```bash
./cpuminerd --address="000000000000000000000000000000000000000000000000000000000000000089eb0d6a8a69" --api="http://localhost:9980/api" --password="sia is cool"
```

## Docker Compose
```yml
services:
  walletd:
    image: ghcr.io/siafoundation/walletd:master
    ports:
      - localhost:9980:9980
      - 9981:9981
    volumes:
      - ./wallet:/data
    restart: unless-stopped
  cpu-miner:
    image: ghcr.io/siafoundation/cpuminer:master
    command: --address="000000000000000000000000000000000000000000000000000000000000000089eb0d6a8a69" --api="http://walletd:9980/api" --password="sia is cool"
    restart: unless-stopped
```
