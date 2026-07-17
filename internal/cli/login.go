package cli

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"
)

// defaultClientID identifies this CLI to the control plane's device flow.
const defaultClientID = "warden-cli"

// defaultIssuer is the Nyxtra control-plane base URL baked into the binary —
// customers never supply it, exactly like `gh` knows github.com.
// Resolution order: --issuer/--dev > WARDEN_ISSUER > last login's issuer >
// (WARDEN_ENV=dev ? devIssuer : this). WARDEN_ENV usually comes from
// ~/.warden/env or a repo .env — see dotenv.go.
const defaultIssuer = "https://nyxtra.dev"

// devIssuer is where WARDEN_ENV=dev (or `login --dev`) points: the local
// Nyxtra dev server (`npm run dev` in the Nyxtra repo).
const devIssuer = "http://localhost:5173"

var (
	flagLoginIssuer       string
	flagLoginClientID     string
	flagLoginNoBrowser    bool
	flagLoginControlPlane string
	flagLoginDev          bool
)

func loginCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "login",
		Short: "Authenticate to the Nyxtra platform (device flow) and store a token",
		Long: `Runs the OAuth 2.0 Device Authorization Grant against the Nyxtra control
plane. Prints a verification URL and opens your browser; enter the code, sign
in through the normal Nyxtra portal, and approve. The CLI stores the resulting
token in ~/.warden/credentials.json, and every other warden command
authenticates with it automatically.`,
		RunE: runLogin,
	}
	c.Flags().StringVar(&flagLoginIssuer, "issuer", "", "Nyxtra base URL (default: $WARDEN_ISSUER, else "+defaultIssuer+")")
	c.Flags().BoolVar(&flagLoginDev, "dev", false, "sign in to the local dev server ("+devIssuer+") — shorthand for --issuer")
	c.Flags().StringVar(&flagLoginClientID, "client-id", "", "OAuth client id (default: $WARDEN_CLIENT_ID or "+defaultClientID+")")
	c.Flags().BoolVar(&flagLoginNoBrowser, "no-browser", false, "don't open a browser; just print the verification URL")
	c.Flags().StringVar(&flagLoginControlPlane, "control-plane", "", "control-plane base URL if different from the issuer (default: the issuer)")
	return c
}

func runLogin(_ *cobra.Command, _ []string) error {
	// Re-logins stick to wherever you last signed in, so overrides are a
	// one-time thing (flag/env still win when set).
	previousIssuer := ""
	if c, err := loadCredentials(); err == nil {
		previousIssuer = c.IssuerURL
	}
	fallback := defaultIssuer
	if os.Getenv("WARDEN_ENV") == "dev" {
		fallback = devIssuer
	}
	explicit := flagLoginIssuer
	if flagLoginDev {
		explicit = devIssuer
	}
	issuer := firstNonEmpty(explicit, os.Getenv("WARDEN_ISSUER"), previousIssuer, fallback)
	clientID := firstNonEmpty(flagLoginClientID, os.Getenv("WARDEN_CLIENT_ID"), defaultClientID)

	fmt.Printf("Signing in to %s\n\n", issuer)
	da, err := requestDeviceCode(issuer, clientID)
	if err != nil {
		return fmt.Errorf("couldn't reach Nyxtra at %s: %w\n  (is it running? point at a different deployment with --issuer or WARDEN_ISSUER)", issuer, err)
	}
	// Deliberately the BARE verification URL, not verification_uri_complete:
	// typing the code from the terminal is the possession check — a prefilled
	// page would reduce approval to a blind click.
	verifyURL := firstNonEmpty(da.VerificationURI, da.VerificationURIComplete)
	fmt.Printf("To sign in, open:\n\n    %s\n\n", verifyURL)
	if da.UserCode != "" {
		fmt.Printf("and enter this code:  %s\n\n", da.UserCode)
	}
	if !flagLoginNoBrowser && os.Getenv("WARDEN_NO_BROWSER") == "" {
		if err := openBrowser(verifyURL); err != nil {
			fmt.Printf("(couldn't open a browser automatically: %v)\n", err)
		}
	}
	fmt.Println("Waiting for you to authorize…")

	tr, err := pollForToken(issuer, clientID, da.DeviceCode, da.Interval, da.ExpiresIn)
	if err != nil {
		return err
	}
	creds := &Credentials{
		IssuerURL:       issuer,
		ClientID:        clientID,
		ControlPlaneURL: firstNonEmpty(flagLoginControlPlane, os.Getenv("WARDEN_CONTROL_PLANE"), issuer),
		TokenType:       tr.TokenType,
		AccessToken:     tr.AccessToken,
	}
	if tr.ExpiresIn > 0 {
		creds.Expiry = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	if err := saveCredentials(creds); err != nil {
		return err
	}
	p, _ := credentialsPath()
	who := "your account"
	if me, err := fetchMe(creds.ControlPlaneURL, creds.AccessToken); err == nil && me.User != nil {
		who = me.User.Email
		if me.Org != nil {
			who += " (org: " + me.Org.Slug + ")"
		}
	}
	fmt.Printf("\n✓ Logged in as %s\n  Token saved to %s\n", who, p)
	fmt.Printf("  Deploy rules with:  warden deploy --file <rule>.sac\n")
	return nil
}

func logoutCmd() *cobra.Command {
	var forget bool
	c := &cobra.Command{
		Use:   "logout",
		Short: "Remove stored Nyxtra credentials",
		RunE: func(_ *cobra.Command, _ []string) error {
			// By default drop the token but remember which deployment this
			// machine talks to, so a plain `warden login` returns to the same
			// place. --forget also drops the remembered issuer, so the next
			// login falls back to the default (https://nyxtra.dev) — the way
			// out of a stale dev deployment pinned by a past login.
			if !forget {
				if c, err := loadCredentials(); err == nil && c.IssuerURL != "" {
					stripped := &Credentials{
						IssuerURL:       c.IssuerURL,
						ClientID:        c.ClientID,
						ControlPlaneURL: c.ControlPlaneURL,
					}
					if err := saveCredentials(stripped); err != nil {
						return err
					}
					fmt.Println("Logged out (token removed; still pointed at " + c.IssuerURL + ").")
					fmt.Println("  Use `warden logout --forget` to also reset the target to https://nyxtra.dev.")
					return nil
				}
			}
			if err := clearCredentials(); err != nil {
				return err
			}
			fmt.Println("Logged out (removed stored credentials).")
			return nil
		},
	}
	c.Flags().BoolVar(&forget, "forget", false, "also forget the remembered deployment (next login uses the https://nyxtra.dev default)")
	return c
}

// openBrowser opens url in the user's default browser (best effort).
func openBrowser(url string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
