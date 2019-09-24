package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/big"
	"os"
	"os/exec"
	"path"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
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

// cacheObject: the output of a compilation unit.
// Given that we're not really using a cache, this is increasingly badly named.
type cacheObject struct {
	ABI      string
	DevDoc   DevDoc
	UserDoc  UserDoc
	Name     string
	Bytecode []byte
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

// printLicenses returns this code's license message as an error.
func printLicenses() error {
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
func mainErr() error {
	// Special-case printing out licenses
	if len(os.Args) == 2 && (os.Args[1] == "-license" || os.Args[1] == "--license") {
		return printLicenses()
	}

	// Parse flags
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
	pflag.StringP(
		"optimize-runs",
		"r",
		"1",
		"Runs to optimize solc compilation for. ",
	)

	pflag.Parse()
	if len(pflag.Args()) == 0 {
		fatal(`usage: poke <.sol file> [-c contract-name] [arg...]

To see the licenses of libraries included in poke, run 'poke -license'`)
	}

	inputFile := pflag.Arg(0)
	args := pflag.Args()[1:]
	defaultContractName := false

	var bytes []byte

	// Set contract name from filename, if needed
	if *contractName == "" {
		defaultContractName = true
		*contractName = trimExtension(path.Base(inputFile))
	}

	// Build or fetch EVM bytecode as needed
	if strings.HasSuffix(inputFile, ".sol") {
		var err error
		bytes, err = abigen(inputFile, *contractName)
		if err != nil {
			return xerrors.Errorf("generating Go bindings to solidity ABI: %w", err)
		}
	} else if strings.HasSuffix(inputFile, ".json") {
		var err error
		bytes, err = openCombinedJson(inputFile, *contractName)
		if err != nil {
			return xerrors.Errorf("poke: %w", err)
		}
	} else {
		return xerrors.Errorf("\"%s\" expected to end with either \".sol\" or \".json\"", inputFile)
	}

	build, err := parseJsonBytecode(bytes, *contractName, inputFile, defaultContractName)

	// Get and parse ABI
	theABI, err := abi.JSON(strings.NewReader(build.ABI))
	if err != nil {
		return xerrors.Errorf("parsing ABI: %w", err)
	}
	devDoc := build.DevDoc
	userDoc := build.UserDoc
	name := build.Name
	bytecode := build.Bytecode
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

// openCombinedJson reads compiled bytecode from an already-existing jsonFile.
func openCombinedJson(jsonFile, contractName string) ([]byte, error) {
	compiled, err := ioutil.ReadFile(jsonFile)
	if err != nil {
		return nil, xerrors.Errorf("reading bytecode: %w", err)
	}
	return compiled, nil
}

// abigen compiles the given Solidity file in workDir and returns the compiled bytecode.
func abigen(solFile, contractName string) ([]byte, error) {
	cmd := exec.Command(
		"solc",
		"--optimize",
		"--optimize-runs", getOptimizeRuns(), // performance tradeoff here
		"--combined-json", "abi,bin,userdoc,devdoc",
		solFile,
	)
	cmd.Stderr = os.Stderr
	compiled, err := cmd.Output()
	if err != nil {
		return nil, xerrors.Errorf("solc: %w", err)
	}
	return compiled, nil
}

// trimExtension returns the filename with its filename extension trimmed away.
func trimExtension(filename string) string {
	return strings.TrimSuffix(filename, path.Ext(filename))
}

// parseJsonBytecode takes compiled bytes, as the bytestream output by
// `solc --combined-json abi,bin,userdoc,devdoc` and formats it as a cacheObject.
// contractName: the name of the contract to grab the cached object from
func parseJsonBytecode(compiled []byte, contractName string, inputFile string, defaultContractName bool) (*cacheObject, error) {
	type CompilerOutput struct {
		ABI     string
		Bin     string
		UserDoc string
		DevDoc  string
	}
	var parsed struct {
		Contracts map[string]CompilerOutput
	}
	err := json.NewDecoder(bytes.NewBuffer(compiled)).Decode(&parsed)
	if err != nil {
		return nil, xerrors.Errorf("failed to decode solc output: %w", err)
	}
	inputFile = path.Base(inputFile)

	// choose which contract we care about
	var compilerOutput CompilerOutput
	{
		var compilerOutputs []CompilerOutput

		for fileColonContractName := range parsed.Contracts {
			nameParts := strings.Split(fileColonContractName, ":")
			if trimExtension(path.Base(nameParts[0])) == trimExtension(inputFile) && nameParts[1] == contractName {
				compilerOutputs = append(compilerOutputs, parsed.Contracts[fileColonContractName])
			}
		}
		if len(compilerOutputs) == 0 {
			errStr := fmt.Sprintf("I found no contract named %q\n", contractName)
			if defaultContractName {
				errStr = errStr +
					"By default, I assume that the target contract name has the same name as " +
					"the .sol or .json file.\n You can set a non-default contract name " +
					"manually with the -c flag.\n"
			}
			fatalf(errStr)
		}
		if len(compilerOutputs) > 1 {
			fatalf(
				"I got unexpected output from solc that I do not know how to handle.\n"+
					"The solc output contained %v results for the %v contract in %v, and I do "+
					"not know which one to choose.\n",
				len(compilerOutputs),
				contractName,
				inputFile,
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
