package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/big"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/jeremyschlatter/xdg"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"golang.org/x/xerrors"
)

func main() {
	err := mainErr()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

}

type cacheObject struct {
	ABI      string
	DevDoc   DevDoc
	UserDoc  UserDoc
	Name     string
	Bytecode []byte
	Hash     []byte
}

var solTypes = map[string]struct {
	parser   func(string) interface{}
	toString func(interface{}) string
	goType   func() interface{}
}{
	"address": {
		parser: func(s string) interface{} {
			return parseAddress(s)
		},
		toString: func(i interface{}) string {
			return i.(*common.Address).Hex()
		},
		goType: func() interface{} {
			return &common.Address{}
		},
	},
	"uint256": {
		parser: func(s string) interface{} {
			return parseToAtto(s)
		},
		toString: func(i interface{}) string {
			return toDisplay(*(i.(**big.Int)))
		},
		goType: func() interface{} {
			return new(*big.Int)
		},
	},
	"bool": {
		parser: func(s string) interface{} {
			b, err := strconv.ParseBool(s)
			if err != nil {
				fatalf("failed to parse %q as bool due to %v", s, err)
			}
			return b
		},
		toString: func(i interface{}) string {
			return strconv.FormatBool(*i.(*bool))
		},
		goType: func() interface{} {
			return new(bool)
		},
	},
	"string": {
		parser: func(s string) interface{} {
			return s
		},
		toString: func(i interface{}) string {
			return *i.(*string)
		},
		goType: func() interface{} {
			return new(string)
		},
	},
}

const usageTemplate = `Usage:{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}{{if gt (len .Aliases) 0}}

Aliases:
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

Examples:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}

{{range getCmdBlocks}}
{{.Name}}:{{range .Commands}}
  {{rpad .Name .NamePadding}} {{.Short}}{{end}}
{{end}}{{end}}{{if .HasAvailableLocalFlags}}

Flags:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

Global Flags:
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasHelpSubCommands}}

Additional help topics:{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}
  {{rpad .CommandPath .CommandPathPadding}} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableSubCommands}}

  Use "{{.CommandPath}} [command] --help" for more information about a command.{{end}}
`

func mainErr() error {
	if len(os.Args) == 2 && (os.Args[1] == "-legal" || os.Args[1] == "--legal") {
		var (
			orderedLicenses []license
			seenLicenses    = make(map[string]bool)
		)
		for _, info := range dependencyLicenses {
			if !seenLicenses[info.License[0]] {
				seenLicenses[info.License[0]] = true
				orderedLicenses = append(orderedLicenses, info.License)
			}
		}
		return template.Must(template.New("").Funcs(
			map[string]interface{}{
				"tab": func(s string) string {
					return strings.ReplaceAll(s, "\n", "\n\t")
				},
			},
		).Parse(`
Poke incorporates a number of open-source libraries, licensed under the following terms:

{{range .Deps}}
	{{.Lib}}
	{{- with .Copyright}}
	{{tab .}}
	{{- end}}
	{{index .License 0}}
{{end}}

The text of these licenses is as follows:

{{range .Licenses}}

             ---------------------------------------------

{{index . 1}}
{{end}}
		`)).Execute(
			os.Stdout,
			map[string]interface{}{
				"Deps":     dependencyLicenses,
				"Licenses": orderedLicenses,
			},
		)
	}

	contractName := pflag.StringP(
		"contract",
		"c",
		"",
		"Name of the contract to wrap. Optional if it matches the name of the .sol file.",
	)
	pflag.StringP(
		"from",
		"F",
		defaultKeys[0],
		"Hex-encoded private key to sign transactions with. Defaults to the 0th address in the 0x mnemonic. Use `hardware` to use Trezor/Ledger. ",
	)
	pflag.String(
		"address",
		"",
		fmt.Sprintf("Address of a deployed copy of the contract."),
	)
	pflag.StringP(
		"node",
		"n",
		"http://localhost:8545",
		"URL of an Ethereum node",
	)
	pflag.IntP(
		"gasprice",
		"g",
		0,
		"Gas price to use, in gwei. Defaults to using go-ethereum default estimation algorithm.",
	)
	pflag.String(
		"derivation-path",
		"m/44'/60'/0'/0/0",
		"BIP 32 derivation path to use with hardware wallet. Only used if --from=hardware",
	)

	pflag.Parse()
	if len(pflag.Args()) == 0 {
		fatal(`usage: poke <.sol file> [-c contract-name] [arg...]

To see the licenses of libraries included in poke, run 'poke -legal'`)
	}
	solFile := pflag.Arg(0)
	args := pflag.Args()[1:]

	workDir, err := ioutil.TempDir("", "poke-work-dir")
	if err != nil {
		return xerrors.Errorf("creating temporary working directory: %w", err)
	}
	defer os.RemoveAll(workDir)

	// TODO: support sol-compiler
	cfg := xdg.Paths{
		XDGSuffix: "poke",
	}
	obj, err := func() (*cacheObject, error) {
		// hash the input
		hash := sha256.New()
		b, err := ioutil.ReadFile(solFile)
		if err != nil {
			return nil, xerrors.Errorf("reading %v: %w", solFile, err)
		}
		hash.Write(b)
		buildHash := hash.Sum(nil)
		fname := fmt.Sprintf("%v-%v.gob", solFile, *contractName)
		cache, err := cfg.CacheFile(fname)
		if err == nil {
			// check if hash matches cache
			// TODO: More robust cache invalidation.
			//       In particular, notice when dependencies have changed
			//       even when the top-level contract file hasn't changed.
			//       One way to do this is to parse the import statements
			//       in the .sol files ourselves. Imports that we can successfully
			//       resolve get hashed. Imports that we cannot successfully resolve
			//       automatically invalidate the cache.
			b, err = ioutil.ReadFile(cache)
			if err != nil {
				return nil, xerrors.Errorf("reading cached build output: %w", err)
			}
			// if so, use cache
			var obj cacheObject
			err = gob.NewDecoder(bytes.NewBuffer(b)).Decode(&obj)
			if err != nil {
				return nil, xerrors.Errorf("decoding cached build output: %w", err)
			}
			if bytes.Equal(buildHash, obj.Hash) {
				return &obj, nil
			}
		}
		// else do work and save cache
		obj, err := abigen(solFile, *contractName, workDir)
		if err != nil {
			return nil, xerrors.Errorf("generating Go bindings to solidity ABI: %w", err)
		}
		obj.Hash = buildHash
		fname, err = cfg.EnsureCacheFile(fname)
		if err != nil {
			return nil, xerrors.Errorf("creating cache file: %w", err)
		}
		buf := new(bytes.Buffer)
		err = gob.NewEncoder(buf).Encode(obj)
		if err != nil {
			return nil, xerrors.Errorf("serializing build output to cache: %w", err)
		}
		err = ioutil.WriteFile(fname, buf.Bytes(), 0644)
		if err != nil {
			return nil, xerrors.Errorf("writing build output to cache: %w", err)
		}
		return obj, nil
	}()
	if err != nil {
		return err
	}

	theABI, err := abi.JSON(strings.NewReader(obj.ABI))
	if err != nil {
		return xerrors.Errorf("parsing ABI: %w", err)
	}
	devDoc := obj.DevDoc
	userDoc := obj.UserDoc
	name := obj.Name
	bytecode := obj.Bytecode
	root := cobra.Command{
		Use:   "poke",
		Short: fmt.Sprintf("A command-line interface to interact with arbitrary smart contracts"),
	}
	var calls, transactions []*cobra.Command
	for name, method := range theABI.Methods {
		name, method := name, method
		parts := []string{name}
		for _, input := range method.Inputs {
			if input.Name == "" {
				parts = append(parts, "<"+input.Type.String()+">")
			} else {
				parts = append(parts, "<"+input.Name+">")
			}
		}
		var short, long string
		{
			dev := devDoc.Methods[method.Sig()].Details
			user := userDoc.Methods[method.Sig()].Notice
			short = dev
			if short == "" {
				short = strings.Split(user, "\n")[0]
				short = strings.Split(short, ".")[0]
			}
			long = user
			if dev != "" {
				if long != "" {
					long += "\n\n"
				}
				long += dev
			}
		}
		cmd := &cobra.Command{
			Use:   strings.Join(parts, " "),
			Short: short,
			Long:  long,
			Args:  cobra.ExactArgs(len(method.Inputs)),
			// TODO: check if the deployed bytecode matches the compiled bytecode
			//       if not, we might be pointing at a different contract, which
			//       will by default print a non-helpful error message.
			Run: func(cmd *cobra.Command, args []string) {
				inputs := make([]interface{}, len(args))
				for i, arg := range args {
					inputs[i] = solTypes[method.Inputs[i].Type.String()].parser(arg)
				}
				if method.Const {
					outType := method.Outputs[0].Type.String()
					out := solTypes[outType].goType()

					// TODO: handle tuple outputs / multiple outputs?
					// TODO: handle no outputs
					err := getDeployment(theABI).Call(
						nil,
						out,
						name,
						inputs...,
					)
					check(err, "calling "+name)
					fmt.Println(solTypes[outType].toString(out))
				} else {
					tx, err := getDeployment(theABI).Transact(
						getTxnOpts(),
						name,
						inputs...,
					)
					log(name+"()", tx, theABI, err)
				}
			},
		}
		if method.Const {
			calls = append(calls, cmd)
		} else {
			transactions = append(transactions, cmd)
		}
		root.AddCommand(cmd)
	}
	utilities := []*cobra.Command{
		showEthCmd,
		sendEthCmd,
		addressCmd,
		showGasCmd,
		deployCmd(name, theABI, bytecode),
		codeAtCmd,
	}
	root.AddCommand(utilities...)
	type cmdBlock struct {
		Name     string
		Commands []*cobra.Command
	}
	sort.Slice(calls, func(i, j int) bool {
		return calls[i].Name() < calls[j].Name()
	})
	sort.Slice(transactions, func(i, j int) bool {
		return transactions[i].Name() < transactions[j].Name()
	})
	cobra.AddTemplateFunc("getCmdBlocks", func() []cmdBlock {
		return []cmdBlock{
			{
				"State Reading Calls",
				calls,
			},
			{
				"State-Changing Transactions",
				transactions,
			},
			{
				"Utilities",
				utilities,
			},
		}
	})
	root.SetUsageTemplate(usageTemplate)
	root.SetArgs(args)
	viper.SetEnvPrefix("poke")
	viper.AutomaticEnv()
	pflag.VisitAll(func(f *pflag.Flag) { root.PersistentFlags().AddFlag(f) })
	viper.BindPFlags(root.PersistentFlags())
	defer runExitFuncs()

	return root.Execute()
}

// DevDoc is parsed @dev documentatation.
type DevDoc struct {
	Methods map[string]struct {
		Details string
	}
}

type notice struct {
	Notice string
}

// UserDoc is parsed documentation.
type UserDoc struct {
	Methods map[string]notice
}

func abigen(solFile, contractName string, workDir string) (*cacheObject, error) {
	cmd := exec.Command(
		"solc",
		"--optimize",
		"--optimize-runs", "1000000", // performance tradeoff here
		"--combined-json", "abi,bin,userdoc,devdoc",
		solFile,
	)
	cmd.Stderr = os.Stderr
	compiled, err := cmd.Output()
	if err != nil {
		return nil, xerrors.Errorf("solc: %w", err)
	}
	type CompilerOutput struct {
		ABI     string
		Bin     string
		UserDoc string
		DevDoc  string
	}
	var parsed struct {
		Contracts map[string]CompilerOutput
	}
	err = json.NewDecoder(bytes.NewBuffer(compiled)).Decode(&parsed)
	if err != nil {
		return nil, xerrors.Errorf("failed to decode solc output: %w", err)
	}

	err = os.Chdir(workDir)
	if err != nil {
		return nil, xerrors.Errorf("changing to temporary working directory: %w", err)
	}

	// choose which contract we care about
	var compilerOutput CompilerOutput
	{
		matchName := contractName
		if contractName == "" {
			parts := strings.Split(solFile, "/")
			matchName = strings.TrimSuffix(parts[len(parts)-1], ".sol")
		}
		var compilerOutputs []CompilerOutput
		for fileColonContractName := range parsed.Contracts {
			nameParts := strings.Split(fileColonContractName, ":")
			if nameParts[0] == solFile && nameParts[1] == matchName {
				compilerOutputs = append(compilerOutputs, parsed.Contracts[fileColonContractName])
			}
		}
		if len(compilerOutputs) == 0 {
			if contractName == "" {
				fatalf(
					"No contract named %q in %v.\n"+
						"By default I expect to find a contract with the same name as the .sol file.\n"+
						"You can set a non-default contract name manually with the -c flag.",
					matchName, solFile,
				)
			}
			fatalf(
				"I did not find a contract named %q in %v",
				contractName, solFile,
			)
		}
		if len(compilerOutputs) > 1 {
			fatalf(
				"I'm sorry, I got unexpected output from solc that I do not know how to handle. The solc output contained %v results for the %v contract in %v, and I do not know which one to choose.",
				len(compilerOutputs), matchName, solFile,
			)
		}
		compilerOutput = compilerOutputs[0]
	}

	var devDoc DevDoc
	err = json.Unmarshal([]byte(compilerOutput.DevDoc), &devDoc)
	if err != nil {
		return nil, xerrors.Errorf("unmarshaling devdoc: %w", err)
	}

	var userDoc UserDoc
	{
		userDoc.Methods = make(map[string]notice)
		// solc outputs a different type for the user docs for the constructor than it does for any other method.
		// Most of the following is there to deal with that fact.
		var tmp struct {
			Methods map[string]interface{}
		}
		err = json.Unmarshal([]byte(compilerOutput.UserDoc), &tmp)
		if err != nil {
			return nil, xerrors.Errorf("unmarshaling userdoc: %w", err)
		}
		for name, methodInfo := range tmp.Methods {
			if name == "constructor" {
				userDoc.Methods[name] = notice{methodInfo.(string)}
			} else {
				userDoc.Methods[name] = notice{
					methodInfo.(map[string]interface{})["notice"].(string),
				}
			}
		}
	}

	bytecode, err := hex.DecodeString(compilerOutput.Bin)
	if err != nil {
		return nil, xerrors.Errorf("decoding bytecode: %w", err)
	}

	return &cacheObject{
		ABI:      compilerOutput.ABI,
		DevDoc:   devDoc,
		UserDoc:  userDoc,
		Name:     contractName,
		Bytecode: bytecode,
	}, err
}
