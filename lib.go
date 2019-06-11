package main

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/accounts/usbwallet"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/shopspring/decimal"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/reserve-protocol/trezor"
)

var defaultKeys = []string{
	"f2f48ee19680706196e2e339e5da3491186e0c4c5030670656b0e0164837257d",
	"5d862464fe9303452126c8bc94274b8c5f9874cbd219789b3eb2128075a76f72",
	"df02719c4df8b9b8ac7f551fcb5d9ef48fa27eef7a66453879f4d8fdc6e78fb1",
	"ff12e391b79415e941a94de3bf3a9aee577aed0731e297d5cfa0b8a1e02fa1d0",
	"752dd9cf65e68cfaba7d60225cbdbc1f4729dd5e5507def72815ed0d8abc6249",
	"efb595a0178eb79a8df953f87c5148402a224cdf725e88c0146727c6aceadccd",
	"83c6d2cc5ddcf9711a6d59b417dc20eb48afd58d45290099e5987e3d768f328f",
	"bb2d3f7c9583780a7d3904a2f55d792707c345f21de1bacb2d389934d82796b2",
	"b2fd4d29c1390b71b8795ae81196bfd60293adf99f9d32a0aff06288fcdac55f",
	"23cb7121166b9a2f93ae0b7c05bde02eae50d64449b2cbb42bc84e9d38d6cc89",
}

var exitFuncs []func()

func atExit(f func()) {
	exitFuncs = append(exitFuncs, f)
}

func runExitFuncs() {
	for _, f := range exitFuncs {
		f()
	}
}

func exit(code int) {
	runExitFuncs()
	os.Exit(code)
}

func fatal(a ...interface{}) {
	fmt.Fprintln(os.Stderr, a...)
	exit(1)
}

func fatalf(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, format, a...)
	exit(1)
}

var client *ethclient.Client

func getNode() *ethclient.Client {
	if client == nil {
		var err error
		nodeAddr := viper.GetString("node")
		client, err = ethclient.Dial(nodeAddr)
		check(err, fmt.Sprintf("Failed to connect to Ethereum node (is there a node running at %q?)", nodeAddr))
	}
	return client
}

var (
	singletonAccount accounts.Account
	singletonWallet  accounts.Wallet
)

func openHardwareWallet() (accounts.Wallet, accounts.Account) {
	if singletonWallet != nil {
		return singletonWallet, singletonAccount
	}

	// Open hardware wallet.
	{
		// Check for connected Ledgers and Trezors.
		ledgerHub, err := usbwallet.NewLedgerHub()
		check(err, "calling usbwallet.NewLedgerHub()")
		trezorHub, err := usbwallet.NewTrezorHub()
		check(err, "calling usbwallet.NewTrezorHub()")

		// Collect them into a single list.
		wallets := accounts.NewManager(nil, ledgerHub, trezorHub).Wallets()

		// Don't proceed unless there is exactly one hardware wallet available.
		if len(wallets) == 0 {
			fatal("No hardware wallets found. Is a hardware wallet plugged in? If it's a Ledger, is it unlocked?")
		}
		if len(wallets) > 1 {
			fatalf("%v hardware wallets found, I don't know which to use", len(wallets))
		}

		wallet := wallets[0]

		// "Open" the wallet.
		// This exchanges initial handshake messages with the wallet.
		// On a Trezor, this may require PIN entry.
		err = wallet.Open("")
		if err == usbwallet.ErrTrezorPINNeeded {
			pin, pinErr := trezor.GetPIN("enter PIN")
			check(pinErr, "getting PIN input")
			err = wallet.Open(pin)
		}
		check(err, "opening hardware wallet")

		// Notify the wallet when the program exits.
		atExit(func() {
			wallet.Close()
		})

		singletonWallet = wallet
	}

	// Open account.
	{
		path := viper.GetString("derivation-path")
		if path == "" {
			fatal("`derivation-path` flag is empty, but `from` is set to \"hardware\". I can't use a hardware wallet without a derivation path.")
		}
		if !strings.HasPrefix(path, "m/") {
			fatalf("got invalid derivation-path: %q. Derivation path must start with \"m/\".", path)
		}
		parsed, err := accounts.ParseDerivationPath(path)
		if err != nil {
			fatalf("got invalid derivation-path: %q. %v", path, err)
		}
		singletonAccount, err = singletonWallet.Derive(
			parsed,
			true, // "pin" this account -- needed for the wallet object to recognize it later in "wallet.SignTx"
		)
		if err != nil && err.Error() == "reply lacks public key entry" {
			fatal("Failed to get public key. Is the Ethereum app open on the Ledger?")
		}
		check(err, "deriving account")
	}

	return singletonWallet, singletonAccount
}

func getNetID() *big.Int {
	netID, err := getNode().NetworkID(context.Background())
	check(err, "Failed to get Ethereum network id")
	return netID
}

func getGasPrice() *big.Int {
	gasPriceFlag := viper.GetInt64("gasPrice")

	if gasPriceFlag == 0 {
		price, err := getNode().SuggestGasPrice(context.Background())
		check(err, "retrieving gas price suggestion")
		return price
	}

	price := big.NewInt(gasPriceFlag)
	price.Mul(price, big.NewInt(1e9))
	return price
}

func getTxnOpts() *bind.TransactOpts {
	from := viper.GetString("from")
	var txnOpts *bind.TransactOpts

	if from != "hardware" {
		txnOpts = bind.NewKeyedTransactor(parseKey(from))
	} else {
		wallet, account := openHardwareWallet()
		txnOpts = &bind.TransactOpts{
			From: account.Address,
			Signer: func(
				protocolSigner types.Signer,
				from common.Address,
				tx *types.Transaction,
			) (*types.Transaction, error) {
				if from != account.Address {
					fatalf(
						"unexpected `from` address. from=%v account=%v",
						from.Hex(),
						account.Address.Hex(),
					)
				}
				fmt.Println("Waiting for you to confirm on the hardware wallet...")
				return wallet.SignTx(account, tx, getNetID())
			},
		}
	}

	txnOpts.GasPrice = getGasPrice()

	// TODO: options for bumping or setting the gas limit, maybe the eth value, and maybe even the nonce.
	return txnOpts
}

var deployment *bind.BoundContract

func getDeployment(abi abi.ABI) *bind.BoundContract {
	if deployment == nil {
		address := viper.GetString("address")
		if address == "" {
			fmt.Fprintln(os.Stderr, "No address specified for the contract.")
			fmt.Fprintln(os.Stderr, "To specify an address, set the --address flag or the POKE_ADDRESS environment variable.")
			exit(1)
		}
		deployment = bind.NewBoundContract(hexToAddress(address), abi, getNode(), getNode(), getNode())
	}
	return deployment
}

// TODO (issue #13): It'd be cleaner for the addresses currently named
// "@0" through "@9" on the command line to just be name "0" through
// "9" -- and then this setting (and the later parseKey) need not go
// through os.Getenv.
func init() {
	for i, key := range defaultKeys {
		envVar := "POKE_" + strconv.Itoa(i)
		if os.Getenv(envVar) == "" {
			os.Setenv(envVar, key)
		}
	}
}

// parseKey parses a hex-encoded private key from s.
// Alternatively, if s begins with "@", parseKey parses
// a hex-encoded private key from the environment variable
// named "POKE_<s[1:]>".
func parseKey(s string) *ecdsa.PrivateKey {
	origS := s
	if strings.HasPrefix(s, "@") {
		env := os.Getenv("POKE_" + s[1:])
		if s == "" {
			fatalf(
				"To use a shorthand argument like %q, there should be a non-empty corresponding environment variable called %q\n",
				s,
				"POKE_"+s[1:],
			)
		}
		s = env
	}
	keyBytes, err1 := hex.DecodeString(s)
	key, err2 := crypto.ToECDSA(keyBytes)
	if err1 != nil || err2 != nil {
		fmt.Fprintln(os.Stderr, "Failed to parse private key:", s)
		if strings.HasPrefix(origS, "@") {
			fatalf(
				"(From argument %q, which I expanded using the env var %v)\n",
				origS,
				"POKE_"+origS[1:],
			)
		}
	}
	return key
}

// HexToAddress parses s into a common.Address.
// Unlike go-ethereum's common.HexToAddress, this version
// exits if s is not a valid address encoding.
// This is copied and lightly modifed from:
// https://github.com/reserve-protocol/reserve/tree/686b03e/pkg/eth
func hexToAddress(s string) common.Address {
	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	check(err, fmt.Sprintf("invalid hex string %q", s))
	var address common.Address
	if len(b) != len(address) {
		fatalf("invalid address length: %v", len(b))
	}
	address.SetBytes(b)
	return address
}

// parseAddress parses a hex-encoded address from s.
// Alternatively, if s begins with "@", parseAddress parses
// a hex-encoded private key from the environment variable
// named "POKE_<s[1:]>", then returns the address corresponding
// to that key.
func parseAddress(s string) common.Address {
	if strings.HasPrefix(s, "@") {
		return toAddress(parseKey(s))
	}
	return hexToAddress(s)
}

// parseToAtto reads a decimal-formatted number of tokens and returns that number times 1e18.
func parseToAtto(s string) *big.Int {
	decimals := int32(18)
	if strings.HasPrefix(s, "int:") {
		s = strings.TrimPrefix(s, "int:")
		decimals = 0
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		fatalf("Expected a decimal number, but got %q instead.\n", s)
	}
	return truncateDecimal(d.Shift(decimals))
}

// truncateDecimal truncates d to an integer and returns it as a *big.Int.
func truncateDecimal(d decimal.Decimal) *big.Int {
	coeff := d.Coefficient()
	exp := d.Exponent()
	z := new(big.Int)
	if exp >= 0 {
		// 	coeff * 10 ** exp
		return coeff.Mul(coeff, z.Exp(big.NewInt(10), big.NewInt(int64(exp)), nil))
	}
	// 	coeff / 10 ** -exp
	return coeff.Div(coeff, z.Exp(big.NewInt(10), big.NewInt(int64(-exp)), nil))
}

// toDisplay does not modify the atto amount, yielding a display in atto
func toDisplay(i *big.Int) string {
	return decimal.NewFromBigInt(i, 0).String()
}

func toAddress(key *ecdsa.PrivateKey) common.Address {
	return crypto.PubkeyToAddress(key.PublicKey)
}

func keyToHex(key *ecdsa.PrivateKey) string {
	return hex.EncodeToString(crypto.FromECDSA(key))
}

// log logs the result of a mutator txn to stdout, including that txn's events.
func log(name string, tx *types.Transaction, abi abi.ABI, err error) {
	check(err, name+" failed")
	receipt, err := bind.WaitMined(context.Background(), getNode(), tx)
	check(err, "waiting for "+name+" to be mined")
	if receipt.Status != types.ReceiptStatusSuccessful {
		fatal("transaction reverted")
	}
	deployment := getDeployment(abi)
	if len(receipt.Logs) > 0 {
		fmt.Println("Done. Events:")
		for _, log := range receipt.Logs {
			// TODO: handle logs from dependencies
			for name, event := range abi.Events {
				if log.Topics[0] == event.Id() {
					m := make(map[string]interface{})
					err := deployment.UnpackLogIntoMap(m, name, *log)
					if err == nil {
						fmt.Println("\t" + name)
						for key, value := range m {
							if addr, ok := value.(common.Address); ok {
								value = addr.Hex()
							}
							fmt.Printf("\t\t%v: %v\n", key, value)
						}
					} else {
						fmt.Println("\t" + err.Error())
					}
					break
				}
			}
		}
	} else {
		fmt.Println("< Done. No events generated >")
	}
}

func getAddress() common.Address {
	from := viper.GetString("from")
	if from == "hardware" {
		_, account := openHardwareWallet()
		return account.Address
	}
	return toAddress(parseKey(from))
}

func deployCmd(name string, abi abi.ABI, bytecode []byte) *cobra.Command {
	parts := []string{"deploy"}
	for _, input := range abi.Constructor.Inputs {
		if input.Name == "" {
			parts = append(parts, "<"+input.Type.String()+">")
		} else {
			parts = append(parts, "<"+input.Name+">")
		}
	}
	return &cobra.Command{
		Use:   strings.Join(parts, " "),
		Short: "Deploy a new copy of " + name,
		Args:  cobra.ExactArgs(len(abi.Constructor.Inputs)),
		Run: func(cmd *cobra.Command, args []string) {
			inputs := make([]interface{}, len(args))
			for i, arg := range args {
				inputs[i] = solTypes[abi.Constructor.Inputs[i].Type.String()].parser(arg)
			}
			address, tx, _, err := bind.DeployContract(
				getTxnOpts(),
				abi,
				bytecode,
				getNode(),
				inputs...,
			)
			viper.Set("address", address.Hex())
			log("deployment", tx, abi, err)
			fmt.Println("export POKE_ADDRESS=" + address.Hex())
		},
	}
}

var addressCmd = &cobra.Command{
	Use:     "address",
	Short:   "Get the address corresponding to the `from` account",
	Example: "  poke address\n  poke address -F @1",
	Args:    cobra.ExactArgs(0),
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(getAddress().Hex())
	},
}

var showGasCmd = &cobra.Command{
	Use:   "show-gas",
	Short: "Show the current gas price estimate.",
	Args:  cobra.ExactArgs(0),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()
		gasPrice, err := getNode().SuggestGasPrice(ctx)
		check(err, "retrieving gas price suggestion")
		fmt.Println(gasPrice)
	},
}

var showEthCmd = &cobra.Command{
	Use:   "show-eth <address>",
	Short: "Show ETH balance.",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()
		address := parseAddress(args[0])
		wei, err := getNode().BalanceAt(ctx, address, nil)
		check(err, "retrieving wei balance")
		fmt.Printf("%v atto-ETH\n", wei)
	},
}

var sendEthCmd = &cobra.Command{
	Use:   "send-eth <address> <value>",
	Short: "Send ETH to an address.",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()
		nonce, err := getNode().PendingNonceAt(ctx, getAddress())
		check(err, "retrieving nonce")
		address := parseAddress(args[0])
		attoTokens := parseToAtto(args[1])
		tx, err := getTxnOpts().Signer(
			types.NewEIP155Signer(getNetID()),
			getAddress(),
			types.NewTransaction(
				nonce,
				address,
				attoTokens,
				21000,
				getGasPrice(),
				nil,
			),
		)
		check(err, "signing transaction")
		check(getNode().SendTransaction(ctx, tx), "sending transaction")
		fmt.Printf("Sent %v atto-ETH to %v.\n", attoTokens, address.Hex())
	},
}

var codeAtCmd = &cobra.Command{
	Use:   "code-at <address>",
	Short: "Get contract code at an address",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		code, err := getNode().CodeAt(
			context.Background(),
			parseAddress(args[0]),
			nil,
		)
		check(err, "retrieving code")
		fmt.Println(hex.EncodeToString(code))
	},
}

func check(err error, msg string) {
	if err != nil {
		fatal(msg+":", err)
	}
}
