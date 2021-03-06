poke
---

Poke is a CLI tool for interacting with smart contracts. See https://medium.com/reserve-currency/poke-a-cli-tool-for-ethereum-development-b4bb66f4e438 for an overview. 

# Installation

The easiest way to get started is to [download the latest binary release](https://dl.equinox.io/reserve-protocol/poke/stable). 

To install poke from source, you'll need [Go 1.12+](https://golang.org/dl/), with modules enabled (eg `GO111MODULE=on`). Just clone this repo locally and run `go install` in the repo root:

    git clone https://github.com/reserve-protocol/poke.git
    cd poke
    go install

It may be useful to have [`solc-select`](https://github.com/crytic/solc-select) when you finally decide to run `poke` if you will be using it directly on `.sol` files. Optionally, you can use `poke` directly on `.json`s, though presumably you will have created those with a compiler like `solc` or `solc-select` in the first place as well.  

# Examples

## Directly from Solidity file
Usually you can just run `poke token.sol`, but things can also get complicated. Here's an example:

    SOLC_VERSION=0.4.24 poke ReserveRights.sol -c ReserveRightsToken \
    transfer 0x91c987bf62D25945dB517BDAa840A6c661374402 100 \
    -F hardware \
    -n https://mainnet.infura.io/v3/d884cdc2e05b4f0897f6dffd0bdc1821 \
    --address 0x8762db106b2c2a0bccb3a80d1ed41273552616e8

What's happening here:
- We set the `solc` version to 0.4.24
- We specify the contract name with -c, since in this case the contract name differs from the file name
- We call the `transfer` function, with two arguments: the address and a value
- We use the -F flag to specify to use the hardware key that is plugged in via USB
- We use the -n flag to direct it to mainnet
- We use the --address flag to specify the token address the ReserveRightsToken is deployed at

## Directly on compiler output
If you've already run `solc` with the flag `--combined-json abi,bin,userdoc,devdoc,...`, then instead of naming a Solidity file on the command line, you can instead provide solc's output combined JSON file. For instance, the same example from before would be:

    poke ReserveRights.json -c ReserveRightsToken \
    transfer 0x91c987bf62D25945dB517BDAa840A6c661374402 100 \
    -F hardware \
    -n https://mainnet.infura.io/v3/d884cdc2e05b4f0897f6dffd0bdc1821 \
    --address 0x8762db106b2c2a0bccb3a80d1ed41273552616e8

This may integrate better with workflow build tools like `make`.

# Troubleshooting

If when using a hardware wallet you encounter it skipping over the "Waiting for you to confirm..." step, it's likely because you don't have contract data enabled on your hardware wallet. 
