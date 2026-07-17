// Command warden is the customer-facing CLI for the Nyxtra platform. It builds
// the command tree in internal/cli and executes it; the root command silences
// cobra's own error/usage output so failures are printed here exactly once.
package main

import (
	"fmt"
	"os"

	"warden-cli/internal/cli"
)

func main() {
	if err := cli.Root().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "warden: "+err.Error())
		os.Exit(1)
	}
}
