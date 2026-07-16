package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// meResponse mirrors the control plane's GET /api/me body (shared/api.ts) —
// the one round-trip that answers "who am I, which org, is my tenant up".
type meResponse struct {
	User *struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Name  string `json:"name"`
	} `json:"user"`
	Org *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
		Role string `json:"role"`
	} `json:"org"`
	Subscription string `json:"subscription"`
	Tenant       *struct {
		Slug   string `json:"slug"`
		Status string `json:"status"`
		URL    string `json:"url"`
	} `json:"tenant"`
}

func fetchMe(cpURL, token string) (*meResponse, error) {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(cpURL, "/")+"/api/me", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("reach control plane: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var me meResponse
	if err := json.Unmarshal(b, &me); err != nil {
		return nil, fmt.Errorf("parse /api/me: %w", err)
	}
	return &me, nil
}

// requireLogin loads stored credentials, failing with a friendly message when
// the user hasn't logged in (or the session expired).
func requireLogin() (*Credentials, error) {
	c, err := loadCredentials()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("not logged in (run `warden login`)")
		}
		return nil, err
	}
	if c.AccessToken == "" {
		return nil, fmt.Errorf("not logged in (run `warden login`)")
	}
	if c.expired() {
		return nil, fmt.Errorf("login expired — run `warden login`")
	}
	return c, nil
}

func whoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the identity behind the stored token",
		RunE: func(_ *cobra.Command, _ []string) error {
			c, err := requireLogin()
			if err != nil {
				return err
			}
			me, err := fetchMe(c.ControlPlaneURL, c.AccessToken)
			if err != nil {
				return err
			}
			fmt.Printf("Issuer:  %s\n", c.IssuerURL)
			if me.User != nil {
				fmt.Printf("Email:   %s\n", me.User.Email)
			}
			if me.Org != nil {
				fmt.Printf("Org:     %s (%s, role %s)\n", me.Org.Name, me.Org.Slug, me.Org.Role)
			}
			if !c.Expiry.IsZero() {
				fmt.Printf("Expires: %s\n", c.Expiry.Format(time.RFC3339))
			}
			return nil
		},
	}
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show your tenant's provisioning/subscription state",
		RunE: func(_ *cobra.Command, _ []string) error {
			c, err := requireLogin()
			if err != nil {
				return err
			}
			me, err := fetchMe(c.ControlPlaneURL, c.AccessToken)
			if err != nil {
				return err
			}
			if me.Org == nil {
				fmt.Println("No workspace yet — finish signup in the Nyxtra portal.")
				return nil
			}
			fmt.Printf("Org:          %s (%s)\n", me.Org.Name, me.Org.Slug)
			fmt.Printf("Subscription: %s\n", me.Subscription)
			if me.Tenant == nil {
				fmt.Println("Tenant:       none provisioned")
				return nil
			}
			fmt.Printf("Tenant:       %s (%s)\n", me.Tenant.Slug, me.Tenant.Status)
			if me.Tenant.URL != "" {
				fmt.Printf("Warden:       %s\n", me.Tenant.URL)
			}
			return nil
		},
	}
}
