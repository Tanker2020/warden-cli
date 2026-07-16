package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/spf13/cobra"
)

func awsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "aws",
		Short: "Connect your AWS account so deployed rules can take real actions",
	}
	c.AddCommand(awsConfigureCmd())
	return c
}

var (
	flagAWSKeyID    string
	flagAWSSecret   string
	flagAWSRegion   string
	flagAWSRoleName string
)

func awsConfigureCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "configure",
		Short: "Connect your AWS account (auto-creates the cross-account role)",
		Long: `Connects your AWS account so your deployed .sac rules can take real
actions. You only need your AWS credentials — warden creates the cross-account
IAM role in your account on your behalf and then discards your keys. They are
used locally only and are never sent to the platform. After this, your hosted
SaC uses the scoped role (not your long-lived keys) for all actions.

Requires: warden login.

Example:
  warden aws configure
  warden aws configure --key-id AKIA... --secret *** --role-name sac-tenant`,
		RunE: runAWSConfigure,
	}
	c.Flags().StringVar(&flagAWSKeyID, "key-id", "", "AWS Access Key ID (prompted if omitted)")
	c.Flags().StringVar(&flagAWSSecret, "secret", "", "AWS Secret Access Key (prompted if omitted)")
	c.Flags().StringVar(&flagAWSRegion, "region", "", "AWS region (default: us-east-1)")
	c.Flags().StringVar(&flagAWSRoleName, "role-name", "sac-tenant", "name for the IAM role created in your account")
	return c
}

func runAWSConfigure(_ *cobra.Command, _ []string) error {
	creds, err := requireLogin()
	if err != nil {
		return err
	}
	cpURL := creds.ControlPlaneURL
	if cpURL == "" {
		return fmt.Errorf("no control-plane URL in credentials — run `warden login` again")
	}

	keyID := flagAWSKeyID
	if keyID == "" {
		keyID, err = prompt("AWS Access Key ID")
		if err != nil {
			return err
		}
	}
	secret := flagAWSSecret
	if secret == "" {
		secret, err = promptSecret("AWS Secret Access Key")
		if err != nil {
			return err
		}
	}
	region := flagAWSRegion
	if region == "" {
		region, err = promptDefault("Region", "us-east-1")
		if err != nil {
			return err
		}
	}
	roleName := flagAWSRoleName
	if roleName == "" {
		roleName = "sac-tenant"
	}

	fmt.Println("==> fetching onboarding details from the Nyxtra platform...")
	onboard, err := fetchOnboarding(cpURL, creds.AccessToken)
	if err != nil {
		return fmt.Errorf("fetch onboarding info: %w", err)
	}
	fmt.Printf("    tenant:               %s\n", onboard.TenantID)
	fmt.Printf("    external id:          %s\n", onboard.ExternalID)
	fmt.Printf("    platform principal:   %s\n", onboard.PlatformPrincipalARN)

	fmt.Printf("==> creating IAM role %q in your AWS account...\n", roleName)
	roleARN, err := createCrossAccountRole(keyID, secret, region, roleName, onboard.PlatformPrincipalARN, onboard.ExternalID)
	if err != nil {
		return fmt.Errorf("create IAM role: %w", err)
	}
	fmt.Printf("    role ARN: %s\n", roleARN)

	fmt.Println("==> registering role ARN with Nyxtra (your AWS keys are NOT sent or stored)...")
	if err := registerRoleARN(cpURL, creds.AccessToken, roleARN, region); err != nil {
		return fmt.Errorf("register role: %w", err)
	}

	fmt.Println()
	fmt.Println("✓ AWS connected. Your hosted SaC will use the scoped role for all actions.")
	fmt.Println("  Your AWS keys were used only to create the role and have been discarded.")
	fmt.Printf("  Role: %s\n", roleARN)
	return nil
}

// onboardingResp is the payload returned by GET /api/aws/onboarding.
type onboardingResp struct {
	TenantID             string `json:"tenant_id"`
	ExternalID           string `json:"external_id"`
	PlatformPrincipalARN string `json:"platform_principal_arn"`
}

func fetchOnboarding(cpURL, token string) (*onboardingResp, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		strings.TrimRight(cpURL, "/")+"/api/aws/onboarding", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out onboardingResp
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// createCrossAccountRole creates (or updates) the IAM role using the
// customer's own credentials, setting the trust policy to allow only the
// platform principal to assume it, gated on the tenant's external id.
func createCrossAccountRole(keyID, secret, region, roleName, platformARN, externalID string) (string, error) {
	cfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(keyID, secret, "")),
	)
	if err != nil {
		return "", err
	}
	iamClient := iam.NewFromConfig(cfg)

	trustPolicy := fmt.Sprintf(`{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": {"AWS": %q},
    "Action": "sts:AssumeRole",
    "Condition": {"StringEquals": {"sts:ExternalId": %q}}
  }]
}`, platformARN, externalID)

	permPolicy := `{
  "Version": "2012-10-17",
  "Statement": [
    {"Sid":"IamRolePolicyManagement","Effect":"Allow","Action":["iam:PutRolePolicy","iam:DeleteRolePolicy","iam:AttachRolePolicy","iam:DetachRolePolicy"],"Resource":"*"},
    {"Sid":"Ec2SecurityGroupsAndInstances","Effect":"Allow","Action":["ec2:AuthorizeSecurityGroupIngress","ec2:RevokeSecurityGroupIngress","ec2:AuthorizeSecurityGroupEgress","ec2:RevokeSecurityGroupEgress","ec2:DescribeSecurityGroups","ec2:DescribeInstances","ec2:TerminateInstances"],"Resource":"*"},
    {"Sid":"Wafv2IpSets","Effect":"Allow","Action":["wafv2:GetIPSet","wafv2:UpdateIPSet"],"Resource":"*"},
    {"Sid":"OrganizationsScp","Effect":"Allow","Action":["organizations:AttachPolicy","organizations:DetachPolicy"],"Resource":"*"}
  ]
}`

	ctx := context.Background()

	var roleARN string
	createOut, err := iamClient.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(roleName),
		AssumeRolePolicyDocument: aws.String(trustPolicy),
		MaxSessionDuration:       aws.Int32(3600),
	})
	if err != nil {
		if !isAlreadyExists(err) {
			return "", fmt.Errorf("iam:CreateRole: %w", err)
		}
		fmt.Printf("    role %q already exists — updating trust policy\n", roleName)
		if _, err := iamClient.UpdateAssumeRolePolicy(ctx, &iam.UpdateAssumeRolePolicyInput{
			RoleName:       aws.String(roleName),
			PolicyDocument: aws.String(trustPolicy),
		}); err != nil {
			return "", fmt.Errorf("iam:UpdateAssumeRolePolicy: %w", err)
		}
		getOut, err := iamClient.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(roleName)})
		if err != nil {
			return "", fmt.Errorf("iam:GetRole: %w", err)
		}
		roleARN = aws.ToString(getOut.Role.Arn)
	} else {
		roleARN = aws.ToString(createOut.Role.Arn)
	}

	if _, err := iamClient.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName:       aws.String(roleName),
		PolicyName:     aws.String("sac-actions"),
		PolicyDocument: aws.String(permPolicy),
	}); err != nil {
		return "", fmt.Errorf("iam:PutRolePolicy: %w", err)
	}

	return roleARN, nil
}

// registerRoleARN records the role ARN on the control plane (POST
// /api/aws/role); the tenant is derived server-side from the caller's org.
func registerRoleARN(cpURL, token, roleARN, region string) error {
	payload, _ := json.Marshal(map[string]string{
		"role_arn":   roleARN,
		"aws_region": region,
	})
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		strings.TrimRight(cpURL, "/")+"/api/aws/role", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// isAlreadyExists reports whether an IAM error indicates the resource exists.
func isAlreadyExists(err error) bool {
	return err != nil && strings.Contains(err.Error(), "EntityAlreadyExists")
}

// prompt reads a line from stdin with a label.
func prompt(label string) (string, error) {
	fmt.Printf("%s: ", label)
	var v string
	_, err := fmt.Fscanln(os.Stdin, &v)
	return strings.TrimSpace(v), err
}

// promptDefault reads a line from stdin, returning def if the user presses enter.
func promptDefault(label, def string) (string, error) {
	fmt.Printf("%s [%s]: ", label, def)
	var v string
	n, err := fmt.Fscanln(os.Stdin, &v)
	if n == 0 || strings.TrimSpace(v) == "" {
		return def, nil
	}
	return strings.TrimSpace(v), err
}

// promptSecret reads a secret from stdin (not echoed in most non-tty contexts).
func promptSecret(label string) (string, error) {
	fmt.Printf("%s: ", label)
	var v string
	_, err := fmt.Fscanln(os.Stdin, &v)
	return strings.TrimSpace(v), err
}
