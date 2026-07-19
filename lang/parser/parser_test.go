package parser

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseExample(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("testdata", "fixtures", "quarantine.sac"))
	if err != nil {
		t.Fatal(err)
	}
	f, diags := ParseFile("quarantine.sac", src)
	if diags.HasErrors() {
		t.Fatalf("unexpected errors: %s", diags.Error())
	}
	if len(f.IfBlocks) != 1 {
		t.Fatalf("want 1 if-block, got %d", len(f.IfBlocks))
	}
	ifb := f.IfBlocks[0]
	if len(ifb.Blocks) != 3 {
		t.Fatalf("want 3 blocks, got %d", len(ifb.Blocks))
	}
	want := []string{"resource.aws_vpc.quarantine", "aws_policy.iam_deny.compromised_role", "aws_policy.security_group_rule.revoke_ssh"}
	for i, b := range ifb.Blocks {
		if b.Identifier() != want[i] {
			t.Errorf("block %d: got %s want %s", i, b.Identifier(), want[i])
		}
	}
	if ifb.Blocks[1].Attrs["target_arn"] != "arn:aws:iam::123456789012:role/WorkloadRole" {
		t.Errorf("missing target_arn attr: %v", ifb.Blocks[1].Attrs)
	}
}

func TestPlainHCLIsValid(t *testing.T) {
	_, diags := ParseFile("x.sac", []byte(`resource "aws_instance" "a" { ami = "ami-1" }`))
	if diags.HasErrors() {
		t.Fatalf("plain HCL should parse: %s", diags.Error())
	}
}

func TestNestedIfRejected(t *testing.T) {
	src := `if condition("a == 'b'") { if condition("c == 'd'") { resource "x" "y" {} } }`
	_, diags := ParseFile("n.sac", []byte(src))
	if !diags.HasErrors() {
		t.Fatal("nested if should be rejected")
	}
}

func TestSyntaxErrorHasPosition(t *testing.T) {
	_, diags := ParseFile("e.sac", []byte("resource \"x\" {"))
	if !diags.HasErrors() {
		t.Fatal("want error")
	}
}

func TestGuardClauseParsed(t *testing.T) {
	src := `if condition("a == 'b'") guard(debounce("60s"), rate_limit(5, "1h")) { resource "x" "y" {} }`
	f, diags := ParseFile("g.sac", []byte(src))
	if diags.HasErrors() {
		t.Fatalf("unexpected errors: %s", diags.Error())
	}
	if len(f.IfBlocks) != 1 {
		t.Fatalf("want 1 if-block, got %d", len(f.IfBlocks))
	}
	want := `debounce("60s"), rate_limit(5, "1h")`
	if got := f.IfBlocks[0].GuardSpec; got != want {
		t.Errorf("GuardSpec: got %q want %q", got, want)
	}
}

func TestNoGuardClauseLeavesSpecEmpty(t *testing.T) {
	src := `if condition("a == 'b'") { resource "x" "y" {} }`
	f, diags := ParseFile("g2.sac", []byte(src))
	if diags.HasErrors() {
		t.Fatalf("unexpected errors: %s", diags.Error())
	}
	if got := f.IfBlocks[0].GuardSpec; got != "" {
		t.Errorf("GuardSpec: got %q want empty", got)
	}
}

func TestGuardClauseMissingParenRejected(t *testing.T) {
	src := `if condition("a == 'b'") guard { resource "x" "y" {} }`
	_, diags := ParseFile("g3.sac", []byte(src))
	if !diags.HasErrors() {
		t.Fatal("want error for guard without (")
	}
}

func TestGuardClauseUnterminatedRejected(t *testing.T) {
	src := `if condition("a == 'b'") guard(debounce("60s") { resource "x" "y" {} }`
	_, diags := ParseFile("g4.sac", []byte(src))
	if !diags.HasErrors() {
		t.Fatal("want error for unterminated guard(...)")
	}
}

func TestAgentDirectiveParsed(t *testing.T) {
	src := "if condition(\"a == 'b'\") guard(notify(\"oncall\")) {\n  !agent nightwatch\n  resource \"x\" \"y\" {}\n}\n"
	f, diags := ParseFile("a.sac", []byte(src))
	if diags.HasErrors() {
		t.Fatalf("unexpected errors: %s", diags.Error())
	}
	if len(f.IfBlocks) != 1 {
		t.Fatalf("want 1 if-block, got %d", len(f.IfBlocks))
	}
	ifb := f.IfBlocks[0]
	if len(ifb.Agents) != 1 {
		t.Fatalf("want 1 agent directive, got %d", len(ifb.Agents))
	}
	if ifb.Agents[0].Name != "nightwatch" {
		t.Errorf("agent name: got %q want nightwatch", ifb.Agents[0].Name)
	}
	if ifb.Agents[0].Line != 2 {
		t.Errorf("agent line: got %d want 2", ifb.Agents[0].Line)
	}
	// The directive line must be stripped so the HCL body still parses and the
	// remaining resource block is recovered.
	if len(ifb.Blocks) != 1 || ifb.Blocks[0].Identifier() != "resource.x.y" {
		t.Fatalf("want 1 resource block, got %+v", ifb.Blocks)
	}
}

func TestAgentDirectiveNotifyOnly(t *testing.T) {
	src := "if condition(\"a == 'b'\") {\n  !agent nightwatch\n}\n"
	f, diags := ParseFile("a2.sac", []byte(src))
	if diags.HasErrors() {
		t.Fatalf("unexpected errors: %s", diags.Error())
	}
	ifb := f.IfBlocks[0]
	if len(ifb.Agents) != 1 || ifb.Agents[0].Name != "nightwatch" {
		t.Fatalf("want nightwatch agent, got %+v", ifb.Agents)
	}
	if len(ifb.Blocks) != 0 {
		t.Fatalf("want 0 blocks, got %d", len(ifb.Blocks))
	}
}

func TestAgentDirectiveArgsCaptured(t *testing.T) {
	src := "if condition(\"a == 'b'\") {\n  !agent nightwatch page=oncall\n}\n"
	f, diags := ParseFile("a3.sac", []byte(src))
	if diags.HasErrors() {
		t.Fatalf("unexpected errors: %s", diags.Error())
	}
	if got := f.IfBlocks[0].Agents[0].Args; got != "page=oncall" {
		t.Errorf("agent args: got %q want %q", got, "page=oncall")
	}
}

func TestBangAgentInStringNotADirective(t *testing.T) {
	// `!agent` inside a string value is not at line-start after trimming, so it
	// must not be mistaken for a directive.
	src := "if condition(\"a == 'b'\") {\n  resource \"x\" \"y\" { note = \"!agent nightwatch\" }\n}\n"
	f, diags := ParseFile("a4.sac", []byte(src))
	if diags.HasErrors() {
		t.Fatalf("unexpected errors: %s", diags.Error())
	}
	if len(f.IfBlocks[0].Agents) != 0 {
		t.Fatalf("want 0 agent directives, got %+v", f.IfBlocks[0].Agents)
	}
}

func TestAgentDirectiveInlineParams(t *testing.T) {
	src := "if condition(\"a == 'b'\") {\n  !agent nightwatch channels=[issues,live] history_window=6h cooldown=1h\n}\n"
	f, diags := ParseFile("p1.sac", []byte(src))
	if diags.HasErrors() {
		t.Fatalf("unexpected errors: %s", diags.Error())
	}
	p := f.IfBlocks[0].Agents[0].Params
	if got := p["channels"]; got != "issues,live" {
		t.Errorf("channels: got %q want %q", got, "issues,live")
	}
	if got := p["history_window"]; got != "6h" {
		t.Errorf("history_window: got %q want %q", got, "6h")
	}
	if got := p["cooldown"]; got != "1h" {
		t.Errorf("cooldown: got %q want %q", got, "1h")
	}
}

func TestAgentDirectiveParenMultilineParams(t *testing.T) {
	src := "if condition(\"a == 'b'\") {\n" +
		"  !agent nightwatch (\n" +
		"    channels=[issues,live],\n" +
		"    slack_lookback=24h,\n" +
		"    tools=[history, reporter],\n" +
		"    cooldown=1h,\n" +
		"  )\n" +
		"  resource \"x\" \"y\" {}\n" +
		"}\n"
	f, diags := ParseFile("p2.sac", []byte(src))
	if diags.HasErrors() {
		t.Fatalf("unexpected errors: %s", diags.Error())
	}
	ifb := f.IfBlocks[0]
	if len(ifb.Agents) != 1 {
		t.Fatalf("want 1 agent, got %d", len(ifb.Agents))
	}
	p := ifb.Agents[0].Params
	if got := p["channels"]; got != "issues,live" {
		t.Errorf("channels: got %q want %q", got, "issues,live")
	}
	if got := p["slack_lookback"]; got != "24h" {
		t.Errorf("slack_lookback: got %q want %q", got, "24h")
	}
	if got := p["tools"]; got != "history, reporter" {
		t.Errorf("tools: got %q want %q", got, "history, reporter")
	}
	if got := p["cooldown"]; got != "1h" {
		t.Errorf("cooldown: got %q want %q", got, "1h")
	}
	// The multi-line directive block must be stripped so the HCL body still parses.
	if len(ifb.Blocks) != 1 || ifb.Blocks[0].Identifier() != "resource.x.y" {
		t.Fatalf("want 1 resource block, got %+v", ifb.Blocks)
	}
}
