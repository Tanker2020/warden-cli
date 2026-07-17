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
	flagDeployFile       string
	flagDeploySacDir     string
	flagDeployList       bool
	flagDeployRemove     bool
	flagDeployToken      string
	flagDeployCP         string
	flagDeployTarget     string
	flagDeployRulesToken string
	flagDeploySacVars    string
)

func deployCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "deploy",
		Short: "Push .sac rules to your hosted environment (after `warden login`)",
		Long: `Deploys rules to a SaC container. Every file is validated locally first
(same parser the container runs), then pushed; the container hot-reloads
without downtime.

Two targeting modes:

  Hosted (default) — via the Nyxtra control plane, after ` + "`warden login`" + `:
    warden deploy --file rule.sac              add/update one rule (additive)
    warden deploy                              push the whole .sac/ bundle
    warden deploy --list                       show the rules currently live
    warden deploy --file rule.sac --remove     undeploy one rule

  Direct (self-hosted) — straight to a container you run yourself (e.g. an OVH
  box), no login/control plane. Point --target at the container's SAC address
  and pass its X-SAC-Token; a .sacvars file rides along so the container's LLM
  reporter and other secret-derived tools reflect your deploy:
    warden deploy --target http://host:8080 --rules-token TOK --file rule.sac
    warden deploy --target http://host:8080 --rules-token TOK           # full bundle
    warden deploy --target http://host:8080 --rules-token TOK --list
    warden deploy --target http://host:8080 --rules-token TOK --file rule.sac --remove`,
		RunE: runDeploy,
	}
	c.Flags().StringVar(&flagDeployFile, "file", "", "path to a single .sac file to deploy")
	c.Flags().StringVar(&flagDeploySacDir, "sac-dir", "", "directory of .sac files to deploy as the full bundle (default: ./.sac, else .)")
	c.Flags().BoolVar(&flagDeployList, "list", false, "list the .sac rules currently live in the target container")
	c.Flags().BoolVar(&flagDeployRemove, "remove", false, "undeploy the --file rule instead of adding it")
	c.Flags().StringVar(&flagDeployToken, "token", "", "bearer token override (default: stored login, or $WARDEN_TOKEN)")
	c.Flags().StringVar(&flagDeployCP, "control-plane", "", "control-plane base URL override (default: stored login)")
	c.Flags().StringVar(&flagDeployTarget, "target", "", "deploy directly to a self-hosted container's SAC base URL (e.g. http://host:8080), bypassing the control plane")
	c.Flags().StringVar(&flagDeployRulesToken, "rules-token", "", "X-SAC-Token for a --target container's /rules (default: $WARDEN_RULES_TOKEN; empty if the container runs tokenless)")
	c.Flags().StringVar(&flagDeploySacVars, "sacvars", "", "path to a .sacvars file to push with a --target deploy (default: auto-discover <sac-dir>/.sacvars or ./.sac/.sacvars)")
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
	// Direct (self-hosted) mode: talk straight to a container's /rules, no
	// login/control plane. Selected by --target.
	if flagDeployTarget != "" {
		return runDeployDirect()
	}
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

// ---------------------------------------------------------------------------
// Direct (self-hosted) deploy: talk straight to a container's /rules endpoint,
// bypassing the Nyxtra control plane. Same wire contract the control plane uses
// (POST {files, sacvars} / GET ?content=1), so a customer can `warden deploy`
// to their own box (e.g. an OVH node) with just its URL + X-SAC-Token.
// ---------------------------------------------------------------------------

// directTarget resolves the container base URL (--target) and the X-SAC-Token
// (--rules-token, else $WARDEN_RULES_TOKEN). The token may be empty when the
// container runs tokenless (SAC_RULES_TOKEN/SAC_WEBHOOK_TOKEN unset).
func directTarget() (base, token string) {
	base = strings.TrimRight(flagDeployTarget, "/")
	token = flagDeployRulesToken
	if token == "" {
		token = os.Getenv("WARDEN_RULES_TOKEN")
	}
	return base, token
}

type directRule struct {
	Name     string `json:"name"`
	IfBlocks int    `json:"if_blocks"`
	Content  string `json:"content,omitempty"`
}

func runDeployDirect() error {
	base, token := directTarget()
	if flagDeployList {
		rules, err := getRulesDirect(base, token, false)
		if err != nil {
			return err
		}
		if len(rules) == 0 {
			fmt.Printf("No rules are live on %s yet.\n", base)
			fmt.Printf("Deploy one with:  warden deploy --target %s --rules-token <tok> --file <rule>.sac\n", base)
			return nil
		}
		fmt.Printf("Live rules on %s:\n\n", base)
		for _, ru := range rules {
			fmt.Printf("  %-40s %d if-block(s)\n", ru.Name, ru.IfBlocks)
		}
		fmt.Printf("\n%d rule(s) active.\n", len(rules))
		return nil
	}

	// Both add and remove operate on the FULL set the container will hold: the
	// /rules POST replaces every *.sac, so we fetch the live set (with content)
	// and merge our change into it — the stateless-merge trick the control
	// plane uses. A plain bundle deploy (no --file) just sends the local set.
	sacvars, err := resolveDirectSacVars()
	if err != nil {
		return err
	}

	var full map[string]string
	if flagDeployRemove {
		if flagDeployFile == "" {
			return fmt.Errorf("--remove needs --file naming the rule to undeploy")
		}
		name := filepath.Base(flagDeployFile)
		if !strings.HasSuffix(name, ".sac") {
			return fmt.Errorf("--file must be a .sac file: %s", flagDeployFile)
		}
		live, err := getRulesDirect(base, token, true)
		if err != nil {
			return err
		}
		full = map[string]string{}
		for _, ru := range live {
			if ru.Name != name {
				full[ru.Name] = ru.Content
			}
		}
		fmt.Printf("==> undeploying %q from %s\n", name, base)
	} else if flagDeployFile != "" {
		// Additive single-file deploy: live set + this file.
		one, err := gatherRules() // honours --file, validates the path
		if err != nil {
			return err
		}
		live, err := getRulesDirect(base, token, true)
		if err != nil {
			return err
		}
		full = map[string]string{}
		for _, ru := range live {
			full[ru.Name] = ru.Content
		}
		for n, c := range one {
			full[n] = c
		}
		fmt.Printf("==> deploying %d rule file(s) to %s\n", len(one), base)
	} else {
		// Full-bundle deploy: the local .sac/ set replaces what's live.
		full, err = gatherRules()
		if err != nil {
			return err
		}
		fmt.Printf("==> deploying %d rule file(s) (full bundle) to %s\n", len(full), base)
	}

	// Validate everything locally before any network call — the container
	// re-validates authoritatively, but syntax errors shouldn't cost a trip.
	for name, content := range full {
		if _, err := validateSource(name, []byte(content), false); err != nil {
			return fmt.Errorf("validation failed (nothing deployed): %w", err)
		}
	}

	res, err := postRulesDirect(base, token, full, sacvars)
	if err != nil {
		return err
	}
	fmt.Printf("\n✓ %d rule(s) now active on %s (%d if-block(s))\n", res.Files, base, res.IfBlocks)
	if res.SacVarsUpdated {
		fmt.Println("  .sacvars applied live (LLM reporter / secret-derived tools refreshed)")
	} else if sacvars != nil {
		fmt.Println("  note: container reported no .sacvars update (no SacVarsPath configured on it)")
	}
	fmt.Printf("  Send events to:  %s/events\n", base)
	return nil
}

// resolveDirectSacVars returns the .sacvars body to push with a direct deploy:
// an explicit --sacvars path (error if missing), else the first auto-discovered
// candidate, else nil (leave the container's file untouched).
func resolveDirectSacVars() (*string, error) {
	if flagDeploySacVars != "" {
		b, err := os.ReadFile(flagDeploySacVars)
		if err != nil {
			return nil, fmt.Errorf("read --sacvars %s: %w", flagDeploySacVars, err)
		}
		s := string(b)
		return &s, nil
	}
	candidates := []string{
		filepath.Join(".sac", ".sacvars"),
		".sacvars",
	}
	if flagDeploySacDir != "" {
		candidates = append([]string{filepath.Join(flagDeploySacDir, ".sacvars")}, candidates...)
	}
	if flagDeployFile != "" {
		candidates = append([]string{filepath.Join(filepath.Dir(flagDeployFile), ".sacvars")}, candidates...)
	}
	for _, p := range candidates {
		if b, err := os.ReadFile(p); err == nil {
			s := string(b)
			fmt.Printf("==> including .sacvars from %s\n", p)
			return &s, nil
		}
	}
	return nil, nil
}

func getRulesDirect(base, token string, withContent bool) ([]directRule, error) {
	url := base + "/rules"
	if withContent {
		url += "?content=1"
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("X-SAC-Token", token)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("reach target %s: %w", base, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("list failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		Rules []directRule `json:"rules"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out.Rules, nil
}

type rulesPostResult struct {
	Files          int  `json:"files"`
	IfBlocks       int  `json:"if_blocks"`
	SacVarsUpdated bool `json:"sacvars_updated"`
}

func postRulesDirect(base, token string, files map[string]string, sacvars *string) (rulesPostResult, error) {
	payload := map[string]any{"files": files}
	if sacvars != nil {
		payload["sacvars"] = *sacvars
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return rulesPostResult{}, err
	}
	req, err := http.NewRequest(http.MethodPost, base+"/rules", bytes.NewReader(body))
	if err != nil {
		return rulesPostResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-SAC-Token", token)
	}
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return rulesPostResult{}, fmt.Errorf("reach target %s: %w", base, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return rulesPostResult{}, fmt.Errorf("deploy failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var res rulesPostResult
	_ = json.Unmarshal(raw, &res)
	return res, nil
}
