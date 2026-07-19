package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"warden-cli/lang/classifier"
	"warden-cli/lang/parser"
)

var flagValidateStrict bool

func validateCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "validate <file.sac>",
		Short: "Parse and lint a .sac file locally; report syntax and classification errors",
		Long: `Runs the embedded SaC language front-end against a .sac file: syntax
errors, action classification, and flap-prone-condition lint (hysteresis)
warnings — all locally, no network. The same parser runs authoritatively on
your hosted container at deploy time; validate catches problems before the
upload.`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			src, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			warnings, err := validateSource(args[0], src, true)
			if err != nil {
				return err
			}
			if flagValidateStrict && warnings > 0 {
				return fmt.Errorf("strict mode: %d warning(s)", warnings)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&flagValidateStrict, "strict", false, "fail on warnings")
	return c
}

// validateSource parses + lints one .sac source, printing diagnostics. verbose
// additionally prints per-block classifications and the OK summary (the
// `warden validate` output); deploy's pre-upload check runs with verbose=false
// so it only surfaces problems. Returns the warning count; a non-nil error
// means the file does not parse.
func validateSource(name string, src []byte, verbose bool) (int, error) {
	f, diags := parser.ParseFile(name, src)
	if diags.HasErrors() {
		for _, d := range diags {
			fmt.Printf("error: %s @ line %d:%d — %s\n", d.Summary, d.Subject.Start.Line, d.Subject.Start.Column, d.Detail)
		}
		return 0, fmt.Errorf("%s: %d error(s)", name, len(diags))
	}
	warnings := 0
	for i := range f.IfBlocks {
		ws, err := classifier.ClassifyIf(&f.IfBlocks[i])
		if err != nil {
			return warnings, fmt.Errorf("%s: classification: %w", name, err)
		}
		for _, w := range ws {
			fmt.Printf("warning: %s: %s\n", name, w)
			warnings++
		}
		if verbose {
			for _, b := range f.IfBlocks[i].Blocks {
				fmt.Printf("  %-7s %s\n", b.Classification, b.Identifier())
			}
		}
	}
	// Hosted rules always run under `sac serve`, so lint in server mode.
	for _, w := range lintGuards(f, true) {
		fmt.Printf("warning: %s: %s\n", name, w)
		warnings++
	}
	if verbose {
		fmt.Printf("OK: %d if-block(s), %d warning(s)\n", len(f.IfBlocks), warnings)
	}
	return warnings, nil
}
