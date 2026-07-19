// Package classifier assigns each block in an if-block to the INFRA or POLICY
// execution path using the priority rules and lookup table in PRD §5.3/§5.4.
package classifier

import (
	"fmt"

	"warden-cli/lang/parser"
)

// PolicyResourceTypes is the initial POLICY_RESOURCE_TYPES lookup table (§5.4).
var PolicyResourceTypes = map[string]bool{
	// IAM
	"aws_iam_policy": true, "aws_iam_role_policy": true, "aws_iam_role_policy_attachment": true,
	"aws_iam_user_policy": true, "aws_iam_group_policy": true,
	// EC2 / VPC
	"aws_security_group": true, "aws_security_group_rule": true,
	"aws_network_acl": true, "aws_network_acl_rule": true,
	// WAF
	"aws_wafv2_web_acl": true, "aws_wafv2_rule_group": true, "aws_wafv2_ip_set": true,
	// Organizations
	"aws_organizations_policy": true, "aws_organizations_policy_attachment": true,
	// Config
	"aws_config_remediation_configuration": true,
	// CloudTrail (tags only)
	"aws_cloudtrail": true,
}

// BoundaryTypes appear in both lists depending on intent; using them without an
// explicit type override yields a warning (§5.4 note).
var BoundaryTypes = map[string]bool{
	"aws_security_group": true,
	"aws_cloudtrail":     true,
}

// Result holds a single block's classification outcome.
type Result struct {
	Block   parser.Block
	Warning string
}

// Classify returns the class for a block plus an optional warning, or an error
// if the type cannot be determined.
func Classify(b parser.Block) (parser.BlockClass, string, error) {
	switch b.Attrs["type"] {
	case "policy":
		return parser.ClassPolicy, "", nil
	case "infra":
		return parser.ClassInfra, "", nil
	}
	if b.Type == "aws_policy" {
		return parser.ClassPolicy, "", nil
	}
	if (b.Type == "resource" || b.Type == "data") && len(b.Labels) > 0 {
		rt := b.Labels[0]
		if PolicyResourceTypes[rt] {
			var warn string
			if BoundaryTypes[rt] {
				warn = fmt.Sprintf("boundary resource %q classified as POLICY; set type = \"infra\" or type = \"policy\" to be explicit", rt)
			}
			return parser.ClassPolicy, warn, nil
		}
	}
	switch b.Type {
	case "resource", "module", "data", "variable", "output", "locals":
		return parser.ClassInfra, "", nil
	}
	return parser.ClassUnknown, "", fmt.Errorf("cannot classify block %q: not aws_policy, not a known infra keyword, and no explicit type set", b.Identifier())
}

// ClassifyIf classifies every block in an if-block, mutating Classification.
func ClassifyIf(ifb *parser.IfBlock) ([]string, error) {
	var warnings []string
	for i := range ifb.Blocks {
		cls, warn, err := Classify(ifb.Blocks[i])
		if err != nil {
			return warnings, err
		}
		ifb.Blocks[i].Classification = cls
		if warn != "" {
			warnings = append(warnings, warn)
		}
	}
	return warnings, nil
}
