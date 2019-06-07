poke
---

Poke is a CLI tool for interacting with smart contracts.

# Installation

The easiest way to get started is to [download the latest binary release](https://dl.equinox.io/reserve-protocol/poke/stable).

If you prefer, you can also install poke from source. You'll need [Go 1.12+](https://golang.org/dl/), with modules enabled (eg `GO111MODULE=on`). Just clone this repo locally and run `go install` in the repo root:

    git clone https://github.com/reserve-protocol/poke.git
    cd poke
    go install

Also, you'll need `solc` installed if you don't have it already, which you can get from `npm`. 

# Examples

Usually you can just run `poke token.sol`, but things can also get complicated. Here's an example where it gets more complicated:

    SOLC_VERSION=0.4.24 poke ReserveRights.sol -c ReserveRightsToken \
    transfer 0x91c987bf62D25945dB517BDAa840A6c661374402 100 \
    -F hardware \
    -n https://mainnet.infura.io/v3/d884cdc2e05b4f0897f6dffd0bdc1821 \
    --address 0x8762db106b2c2a0bccb3a80d1ed41273552616e8

What's happening here:
- We change the `solc` version to 0.4.24
- We specify the contract name, since in this case the contract name differs from the file name
- We call the `transfer` function, with two arguments: the address and a value
- We use the -F flag to specify to use the hardware key that is plugged in via USB
- We use the -n flag to direct it to mainnet
- We use the --address flag to specify the token address
