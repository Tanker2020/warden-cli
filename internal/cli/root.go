// Package cli implements the warden command — the customer-facing CLI for the
// Nyxtra platform. It authenticates against the Nyxtra control plane (Cloudflare
// Workers + Better Auth) and manages the tenant's hosted SaC + Warden:
//
//	warden login          device-flow sign-in via the Nyxtra web portal
//	warden validate       parse + lint .sac files locally (embedded compiler front-end)
//	warden deploy         push .sac rules to your hosted container (via the control plane)
//	warden test           fire a synthetic event at your hosted ingest endpoint
//	warden status         tenant + subscription state
//	warden aws configure  connect your AWS account (cross-account role, keys never leave your machine)
//
// The SaC engine itself (`sac serve` and the local plan/apply tooling) lives in
// the closed engine repo; this CLI embeds only the language front-end.
package cli

import "github.com/spf13/cobra"

// Version is the build version (set via -ldflags or default).
var Version = "0.1.0-dev"

// Root builds the warden command tree.
func Root() *cobra.Command {
	loadEnvFiles() // ./.env and ~/.warden/env — see dotenv.go
	root := &cobra.Command{
		Use:     "warden",
		Short:   "Warden — manage your hosted SaC + Warden from the terminal",
		Version: Version,
		// Runtime failures (auth, network, server rejections) aren't usage
		// errors — don't dump help, and let main print the error once.
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(
		loginCmd(), logoutCmd(), whoamiCmd(), statusCmd(),
		validateCmd(), deployCmd(), testCmd(), awsCmd(),
	)
	return root
}
