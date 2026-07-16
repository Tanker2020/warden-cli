package cli

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var (
	flagTestAlarm  string
	flagTestRegion string
)

func testCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "test",
		Short: "Send a synthetic event to your hosted ingest endpoint",
		Long: `Fires a CloudWatch-shaped test event at your hosted container's /events
endpoint using the ingest URL + event token recorded by "warden deploy". This
exercises the live pipeline (edge → container → rules), unlike "warden
validate" which is a purely local static check.`,
		RunE: runTest,
	}
	c.Flags().StringVar(&flagTestAlarm, "alarm", "warden-test", "alarmName to put in the test event (match your rule's condition to see it fire)")
	c.Flags().StringVar(&flagTestRegion, "region", "us-east-1", "region field for the test event")
	return c
}

func runTest(_ *cobra.Command, _ []string) error {
	c, err := requireLogin()
	if err != nil {
		return err
	}
	if c.IngestURL == "" || c.EventToken == "" {
		return fmt.Errorf("no ingest endpoint recorded — run `warden deploy` first")
	}
	id := "warden-test-" + shortID()
	body, _ := json.Marshal(map[string]any{
		"id":          id,
		"source":      "aws.cloudwatch",
		"detail-type": "CloudWatch Alarm State Change",
		"region":      flagTestRegion,
		"detail": map[string]any{
			"alarmName": flagTestAlarm,
			"state":     map[string]string{"value": "ALARM"},
		},
	})
	fmt.Printf("==> sending test event %s to %s\n", id, c.IngestURL)
	req, err := http.NewRequest(http.MethodPost, c.IngestURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-SAC-Token", c.EventToken)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	fmt.Printf("    %s %s\n", resp.Status, strings.TrimSpace(string(out)))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("ingest returned HTTP %d", resp.StatusCode)
	}
	fmt.Println("✓ Event accepted. If a rule matches this alarm name, check Warden for the action.")
	return nil
}

func shortID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
