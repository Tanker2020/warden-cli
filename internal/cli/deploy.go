package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var (
	flagDeployFile   string
	flagDeploySacDir string
	flagDeployList   bool
	flagDeployRemove bool
	flagDeployToken  string
	flagDeployCP     string
)

func deployCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "deploy",
		Short: "Push .sac rules to your hosted environment (after `warden login`)",
		Long: `Deploys rules to your org's hosted SaC container via the Nyxtra control
plane. Every file is validated locally first (same parser the container runs),
then pushed; the container hot-reloads without downtime.

  warden deploy --file rule.sac    add/update one rule (additive)
  warden deploy                    push the whole .sac/ bundle (replaces the set)
  warden deploy --list             show the rules currently live
  warden deploy --file rule.sac --remove   undeploy one rule`,
		RunE: runDeploy,
	}
	c.Flags().StringVar(&flagDeployFile, "file", "", "path to a single .sac file to deploy")
	c.Flags().StringVar(&flagDeploySacDir, "sac-dir", "", "directory of .sac files to deploy as the full bundle (default: ./.sac, else .)")
	c.Flags().BoolVar(&flagDeployList, "list", false, "list the .sac rules currently live in your hosted container")
	c.Flags().BoolVar(&flagDeployRemove, "remove", false, "undeploy the --file rule instead of adding it")
	c.Flags().StringVar(&flagDeployToken, "token", "", "bearer token override (default: stored login, or $WARDEN_TOKEN)")
	c.Flags().StringVar(&flagDeployCP, "control-plane", "", "control-plane base URL override (default: stored login)")
	return c
}

// deployTarget resolves the control-plane URL and bearer token for a hosted
// deploy from flags/env/stored login.
func deployTarget() (cpURL, token string, err error) {
	token, err = resolveBearerToken(flagDeployToken)
	if err != nil {
		return "", "", err
	}
	if token == "" {
		return "", "", fmt.Errorf("not logged in — run `warden login`")
	}
	cpURL = flagDeployCP
	if cpURL == "" {
		if c, cerr := loadCredentials(); cerr == nil {
			cpURL = c.ControlPlaneURL
		}
	}
	if cpURL == "" {
		cpURL = os.Getenv("WARDEN_CONTROL_PLANE")
	}
	if cpURL == "" {
		return "", "", fmt.Errorf("no control-plane URL — run `warden login` (or pass --control-plane)")
	}
	return cpURL, token, nil
}

type deployResult struct {
	Tenant     string `json:"tenant"`
	Org        string `json:"org"`
	Email      string `json:"email"`
	WardenURL  string `json:"warden_url"`
	IngestURL  string `json:"ingest_url"`
	EventToken string `json:"event_token"`
	Files      int    `json:"files"`
	Created    bool   `json:"created"`
	Error      string `json:"error"`
}

type listResult struct {
	Tenant     string `json:"tenant"`
	Org        string `json:"org"`
	WardenURL  string `json:"warden_url"`
	IngestURL  string `json:"ingest_url"`
	EventToken string `json:"event_token"`
	Rules      []struct {
		Name     string `json:"name"`
		IfBlocks int    `json:"if_blocks"`
	} `json:"rules"`
	Count int    `json:"count"`
	Error string `json:"error"`
}

func runDeploy(_ *cobra.Command, _ []string) error {
	cpURL, token, err := deployTarget()
	if err != nil {
		return err
	}
	if flagDeployList {
		return runDeployList(cpURL, token)
	}
	if flagDeployRemove {
		return runDeployRemove(cpURL, token)
	}

	files, err := gatherRules()
	if err != nil {
		return err
	}
	// Validate everything locally before any network call — the container
	// re-validates authoritatively, but syntax errors shouldn't cost a trip.
	for name, content := range files {
		if _, err := validateSource(name, []byte(content), false); err != nil {
			return fmt.Errorf("validation failed (nothing deployed): %w", err)
		}
	}

	payload, err := json.Marshal(map[string]any{"files": files})
	if err != nil {
		return err
	}
	fmt.Printf("==> deploying %d rule file(s) to your hosted environment (%s)\n", len(files), cpURL)
	res, err := postDeploy(cpURL, token, payload)
	if err != nil {
		return err
	}
	if res.Created {
		fmt.Printf("\n✓ Provisioned a new hosted environment for %s\n", res.Org)
	} else {
		fmt.Printf("\n✓ Updated your hosted environment (%s)\n", res.Org)
	}
	fmt.Printf("  %d rule(s) now active\n", res.Files)
	if res.WardenURL != "" {
		fmt.Printf("  Review & approve actions:  %s\n", res.WardenURL)
	}
	if res.IngestURL != "" {
		fmt.Printf("  Send events to:            %s\n", res.IngestURL)
	}
	_ = persistHostedEndpoints(res.IngestURL, res.EventToken)
	return nil
}

// persistHostedEndpoints stores the ingest URL + event token next to the login
// credentials so `warden test` can fire events without repo files.
func persistHostedEndpoints(ingestURL, eventToken string) error {
	if ingestURL == "" && eventToken == "" {
		return nil
	}
	c, err := loadCredentials()
	if err != nil {
		return err
	}
	if ingestURL != "" {
		c.IngestURL = ingestURL
	}
	if eventToken != "" {
		c.EventToken = eventToken
	}
	return saveCredentials(c)
}

func runDeployRemove(cpURL, token string) error {
	if flagDeployFile == "" {
		return fmt.Errorf("--remove needs --file naming the rule to undeploy (e.g. --file rule.sac --remove)")
	}
	name := filepath.Base(flagDeployFile)
	if !strings.HasSuffix(name, ".sac") {
		return fmt.Errorf("--file must be a .sac file: %s", flagDeployFile)
	}
	payload, err := json.Marshal(map[string]any{"remove": []string{name}})
	if err != nil {
		return err
	}
	fmt.Printf("==> undeploying %q from your hosted environment (%s)\n", name, cpURL)
	res, err := postDeploy(cpURL, token, payload)
	if err != nil {
		return err
	}
	fmt.Printf("\n✓ Undeployed %q from %s\n", name, res.Org)
	fmt.Printf("  %d rule(s) still active\n", res.Files)
	fmt.Println("  (any cloud resources a removed rule already created are left in place)")
	return nil
}

func runDeployList(cpURL, token string) error {
	url := strings.TrimRight(cpURL, "/") + "/api/deploy"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("reach control plane: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var res listResult
	_ = json.Unmarshal(body, &res)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := res.Error
		if msg == "" {
			msg = strings.TrimSpace(string(body))
		}
		return fmt.Errorf("list failed (%d): %s", resp.StatusCode, msg)
	}
	if res.Count == 0 {
		fmt.Printf("No rules are live in your hosted environment (%s) yet.\n", res.Org)
		fmt.Println("Deploy one with:  warden deploy --file <rule>.sac")
		return nil
	}
	fmt.Printf("Live rules in your hosted environment (%s):\n\n", res.Org)
	for _, ru := range res.Rules {
		fmt.Printf("  %-40s %d if-block(s)\n", ru.Name, ru.IfBlocks)
	}
	fmt.Printf("\n%d rule(s) active.\n", res.Count)
	if res.WardenURL != "" {
		fmt.Printf("Review & approve:  %s\n", res.WardenURL)
	}
	_ = persistHostedEndpoints(res.IngestURL, res.EventToken)
	return nil
}

// postDeploy POSTs a payload to the control plane's /api/deploy endpoint and
// returns the parsed result, turning a non-2xx into an error.
func postDeploy(cpURL, token string, payload []byte) (deployResult, error) {
	url := strings.TrimRight(cpURL, "/") + "/api/deploy"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return deployResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := (&http.Client{Timeout: 4 * time.Minute}).Do(req)
	if err != nil {
		return deployResult{}, fmt.Errorf("reach control plane: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var res deployResult
	_ = json.Unmarshal(body, &res)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := res.Error
		if msg == "" {
			msg = strings.TrimSpace(string(body))
		}
		return deployResult{}, fmt.Errorf("deploy failed (%d): %s", resp.StatusCode, msg)
	}
	return res, nil
}

// gatherRules returns the rule files to deploy: a single --file when given
// (added additively), otherwise every *.sac in the bundle dir (--sac-dir,
// defaulting to ./.sac when it exists, else the working directory).
func gatherRules() (map[string]string, error) {
	if flagDeployFile != "" {
		abs, err := filepath.Abs(flagDeployFile)
		if err != nil {
			return nil, err
		}
		name := filepath.Base(abs)
		if !strings.HasSuffix(name, ".sac") {
			return nil, fmt.Errorf("--file must be a .sac file: %s", flagDeployFile)
		}
		content, err := os.ReadFile(abs)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", flagDeployFile, err)
		}
		return map[string]string{name: string(content)}, nil
	}
	dir := flagDeploySacDir
	if dir == "" {
		if st, err := os.Stat(".sac"); err == nil && st.IsDir() {
			dir = ".sac"
		} else {
			dir = "."
		}
	}
	return readSacBundle(dir)
}

func readSacBundle(dir string) (map[string]string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.sac"))
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, p := range matches {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		out[filepath.Base(p)] = string(b)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no *.sac files in %s (use --file or --sac-dir)", dir)
	}
	return out, nil
}
