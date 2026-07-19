package classifier

import (
	"testing"

	"warden-cli/lang/parser"
)

func cls(t *testing.T, b parser.Block) parser.BlockClass {
	t.Helper()
	c, _, err := Classify(b)
	if err != nil {
		t.Fatalf("classify %s: %v", b.Identifier(), err)
	}
	return c
}

func TestPriorityRules(t *testing.T) {
	if cls(t, parser.Block{Type: "resource", Labels: []string{"aws_vpc"}, Attrs: map[string]string{"type": "policy"}}) != parser.ClassPolicy {
		t.Error("explicit type=policy must win")
	}
	if cls(t, parser.Block{Type: "aws_policy", Labels: []string{"iam_deny"}}) != parser.ClassPolicy {
		t.Error("aws_policy must be POLICY")
	}
	if cls(t, parser.Block{Type: "resource", Labels: []string{"aws_iam_policy"}}) != parser.ClassPolicy {
		t.Error("lookup-table type must be POLICY")
	}
	if cls(t, parser.Block{Type: "resource", Labels: []string{"aws_vpc"}}) != parser.ClassInfra {
		t.Error("plain resource must be INFRA")
	}
	if cls(t, parser.Block{Type: "variable", Labels: []string{"x"}}) != parser.ClassInfra {
		t.Error("variable passthrough INFRA")
	}
}

func TestBoundaryWarning(t *testing.T) {
	_, warn, err := Classify(parser.Block{Type: "resource", Labels: []string{"aws_security_group"}})
	if err != nil || warn == "" {
		t.Fatalf("expected boundary warning, got warn=%q err=%v", warn, err)
	}
	_, warn, _ = Classify(parser.Block{Type: "resource", Labels: []string{"aws_security_group"}, Attrs: map[string]string{"type": "infra"}})
	if warn != "" {
		t.Error("override must suppress boundary warning")
	}
}

func TestUnclassifiable(t *testing.T) {
	if _, _, err := Classify(parser.Block{Type: "frobnicate"}); err == nil {
		t.Error("unknown keyword must error")
	}
}
