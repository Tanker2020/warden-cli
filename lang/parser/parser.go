// Package parser parses .sac files: valid OpenTofu HCL extended with
// `if condition(...) { }` blocks and the aws_policy primitive. PRD §6.1.
package parser

import (
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// BlockClass is the execution path a block is routed to.
type BlockClass string

const (
	ClassUnknown BlockClass = ""
	ClassInfra   BlockClass = "INFRA"
	ClassPolicy  BlockClass = "POLICY"
)

// Block is a single resource/data/module/aws_policy/var/output/locals block
// found inside an if-block.
type Block struct {
	Type           string // keyword: resource, aws_policy, module, data, ...
	Labels         []string
	Attrs          map[string]string // best-effort literal attributes
	Raw            string            // verbatim HCL for patch-file rendering
	Classification BlockClass
	DefLine        int
}

// Identifier returns "type.label0.label1" for logging/audit.
func (b Block) Identifier() string {
	if len(b.Labels) == 0 {
		return b.Type
	}
	return b.Type + "." + strings.Join(b.Labels, ".")
}

// AgentDirective is a `!agent <name> [params]` line declared inside an if-block
// body. It opts that if-block into an agentic workflow (see internal/agent):
// the named agent runs when the condition matches, in addition to any
// INFRA/POLICY sub-blocks. Agents are opt-in — a block with no `!agent` line
// never invokes one.
//
// Params are optional key=value settings. They may be written inline on the
// directive line, or — for readability — wrapped in parentheses and spread
// across multiple lines. Separators are commas and/or whitespace; list values
// use brackets so their internal commas aren't mistaken for separators:
//
//	!agent nightwatch channels=[issues,live] history_window=6h
//
//	!agent nightwatch (
//	  channels       = [issues, live],
//	  history_window = 6h,
//	  tools          = [history, reporter],
//	  cooldown       = 1h,
//	)
type AgentDirective struct {
	Name   string            // agent name, e.g. "nightwatch"
	Args   string            // raw params text (inline line or inside the parens)
	Params map[string]string // parsed key=value params; list values keep internal commas
	Line   int               // 1-based line in the source file
}

// IfBlock is a parsed `if condition("...") { ... }` construct, optionally
// wrapped in a `guard(...)` clause (see internal/guard).
type IfBlock struct {
	Condition string
	GuardSpec string           // raw contents of an optional guard(...) clause
	Agents    []AgentDirective // `!agent ...` directives declared in the body
	Blocks    []Block
	Line      int
}

// File is a parsed .sac file.
type File struct {
	Path     string
	IfBlocks []IfBlock
	Base     []byte // top-level HCL (provider/terraform/etc.) with if-blocks removed
}

// ParseFile parses the bytes of a .sac file. Diagnostics carry line/column.
func ParseFile(path string, src []byte) (*File, hcl.Diagnostics) {
	var diags hcl.Diagnostics
	f := &File{Path: path}

	regions, scanDiags := scanIfBlocks(path, src)
	diags = append(diags, scanDiags...)

	// Pass 1: blank out if-regions and validate the remaining HCL as-is so any
	// valid OpenTofu file is a valid .sac file (SAC-P-01).
	base := blankRegions(src, regions)
	_, hclDiags := hclsyntax.ParseConfig(base, path, hcl.Pos{Line: 1, Column: 1})
	diags = append(diags, hclDiags...)
	f.Base = base

	// Pass 2: parse each if-region body.
	for _, r := range regions {
		ifb, idiags := parseIfRegion(path, src, r)
		diags = append(diags, idiags...)
		if ifb != nil {
			f.IfBlocks = append(f.IfBlocks, *ifb)
		}
	}
	return f, diags
}

func parseIfRegion(path string, src []byte, r region) (*IfBlock, hcl.Diagnostics) {
	// Extract `!agent ...` directives first: they are not valid HCL, so the
	// returned body has each directive line blanked out (newlines preserved)
	// before the remainder is parsed as HCL.
	agents, body := extractAgents(path, src, r.bodyStart, r.bodyEnd)
	// Reject nested if blocks (SAC-P-05).
	if idx := indexIf(body); idx >= 0 {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "nested conditions are not supported",
			Detail:   "an if condition() block may not contain another if condition() block",
			Subject:  posRange(path, src, r.bodyStart+idx),
		}}
	}
	pad := make([]byte, r.bodyStart) // preserve original line numbers
	for i := range pad {
		if src[i] == '\n' {
			pad[i] = '\n'
		} else {
			pad[i] = ' '
		}
	}
	full := append(pad, body...)
	file, d := hclsyntax.ParseConfig(full, path, hcl.Pos{Line: 1, Column: 1})
	if d.HasErrors() {
		return nil, d
	}
	ifb := &IfBlock{Condition: r.condition, GuardSpec: r.guardSpec, Agents: agents, Line: posRange(path, src, r.ifStart).Start.Line}
	hb := file.Body.(*hclsyntax.Body)
	for _, blk := range hb.Blocks {
		ifb.Blocks = append(ifb.Blocks, Block{
			Type:    blk.Type,
			Labels:  blk.Labels,
			Attrs:   literalAttrs(blk.Body),
			Raw:     string(src[blk.TypeRange.Start.Byte:blk.CloseBraceRange.End.Byte]),
			DefLine: blk.TypeRange.Start.Line,
		})
	}
	return ifb, d
}

// literalAttrs extracts best-effort string values of leaf attributes.
func literalAttrs(b *hclsyntax.Body) map[string]string {
	out := map[string]string{}
	for name, attr := range b.Attributes {
		v, d := attr.Expr.Value(nil)
		if d.HasErrors() || v.IsNull() || !v.IsKnown() {
			continue
		}
		if v.Type().IsPrimitiveType() {
			switch v.Type() {
			case cty.String:
				out[name] = v.AsString()
			case cty.Number:
				out[name] = v.AsBigFloat().Text('f', -1)
			case cty.Bool:
				if v.True() {
					out[name] = "true"
				} else {
					out[name] = "false"
				}
			}
		}
	}
	return out
}

func indexIf(b []byte) int {
	s := string(b)
	for _, kw := range []string{"if condition(", "if  condition(", "if\tcondition("} {
		if i := strings.Index(s, kw); i >= 0 {
			return i
		}
	}
	return -1
}

func isIdentByte(b byte) bool {
	return b == '_' || b == '-' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// parseAgentParams turns a raw params string (from an inline directive line or
// the inside of a `(...)` block) into a key=value map. Pairs are separated by
// commas and/or whitespace and may span multiple lines; spaces are allowed
// around the `=`. A value wrapped in [brackets] keeps its internal commas as a
// list; surrounding double quotes are stripped. A `#` starts a comment that
// runs to end-of-line. Keys are lower-cased. Returns nil when there are no
// params.
func parseAgentParams(raw string) map[string]string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	isSep := func(c byte) bool {
		return c == ',' || c == ' ' || c == '\t' || c == '\n' || c == '\r'
	}
	out := map[string]string{}
	i, n := 0, len(raw)
	for i < n {
		c := raw[i]
		if isSep(c) {
			i++
			continue
		}
		if c == '#' { // comment to end of line
			for i < n && raw[i] != '\n' {
				i++
			}
			continue
		}
		// key: read until whitespace, comma, '=', or '#'
		keyStart := i
		for i < n && !isSep(raw[i]) && raw[i] != '=' && raw[i] != '#' {
			i++
		}
		key := strings.ToLower(strings.TrimSpace(raw[keyStart:i]))
		// allow spaces before '='
		for i < n && (raw[i] == ' ' || raw[i] == '\t') {
			i++
		}
		val := ""
		if i < n && raw[i] == '=' {
			i++ // consume '='
			for i < n && (raw[i] == ' ' || raw[i] == '\t') {
				i++
			}
			switch {
			case i < n && raw[i] == '[': // bracketed list: read to matching ]
				depth, vStart := 0, i
				for i < n {
					if raw[i] == '[' {
						depth++
					} else if raw[i] == ']' {
						depth--
						if depth == 0 {
							i++
							break
						}
					}
					i++
				}
				val = strings.TrimSpace(raw[vStart:i])
			case i < n && raw[i] == '"': // quoted string
				vStart := i
				i++
				for i < n && raw[i] != '"' {
					i++
				}
				if i < n {
					i++ // closing quote
				}
				val = raw[vStart:i]
			default: // bare token until separator or comment
				vStart := i
				for i < n && !isSep(raw[i]) && raw[i] != '#' {
					i++
				}
				val = strings.TrimSpace(raw[vStart:i])
			}
		}
		if key == "" {
			continue
		}
		if len(val) >= 2 && val[0] == '[' && val[len(val)-1] == ']' {
			val = strings.TrimSpace(val[1 : len(val)-1])
		}
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		}
		out[key] = val
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// extractAgents scans an if-block body (src[bodyStart:bodyEnd]) for `!agent`
// directive lines and returns them along with a copy of the body in which each
// directive line has been blanked out (newlines preserved) so the remainder is
// still valid HCL. A directive is only recognized when `!` is the first
// non-whitespace character on its line, so `!agent` appearing inside an HCL
// string value or comment is never mistaken for a directive.
func extractAgents(path string, src []byte, bodyStart, bodyEnd int) ([]AgentDirective, []byte) {
	body := src[bodyStart:bodyEnd]
	clean := append([]byte(nil), body...)
	var out []AgentDirective
	n := len(body)
	for i := 0; i < n; {
		lineStart := i
		j := i
		for j < n && (body[j] == ' ' || body[j] == '\t') {
			j++
		}
		if j < n && body[j] == '!' {
			k := j + 1
			wordStart := k
			for k < n && isIdentByte(body[k]) {
				k++
			}
			if string(body[wordStart:k]) == "agent" {
				for k < n && (body[k] == ' ' || body[k] == '\t') {
					k++
				}
				nameStart := k
				for k < n && isIdentByte(body[k]) {
					k++
				}
				name := string(body[nameStart:k])
				// Optional params: either the rest of the line, or a parenthesized
				// block that may span multiple lines.
				p := k
				for p < n && (body[p] == ' ' || body[p] == '\t') {
					p++
				}
				var rawArgs string
				directiveEnd := k
				if p < n && body[p] == '(' {
					depth, q, closeIdx := 0, p, -1
					for q < n {
						if body[q] == '(' {
							depth++
						} else if body[q] == ')' {
							depth--
							if depth == 0 {
								closeIdx = q
								break
							}
						}
						q++
					}
					if closeIdx >= 0 {
						rawArgs = strings.TrimSpace(string(body[p+1 : closeIdx]))
						directiveEnd = closeIdx + 1
					} else {
						rawArgs = strings.TrimSpace(string(body[p+1 : n])) // unterminated: consume to end
						directiveEnd = n
					}
				} else {
					le := k
					for le < n && body[le] != '\n' {
						le++
					}
					rawArgs = strings.TrimSpace(string(body[k:le]))
					directiveEnd = le
				}
				out = append(out, AgentDirective{
					Name:   name,
					Args:   rawArgs,
					Params: parseAgentParams(rawArgs),
					Line:   posRange(path, src, bodyStart+j).Start.Line,
				})
				for b := lineStart; b < directiveEnd; b++ {
					if clean[b] != '\n' {
						clean[b] = ' '
					}
				}
				i = directiveEnd
				continue
			}
		}
		for i < n && body[i] != '\n' {
			i++
		}
		if i < n {
			i++
		}
	}
	return out, clean
}

func posRange(path string, src []byte, byteOff int) *hcl.Range {
	line, col := 1, 1
	for i := 0; i < byteOff && i < len(src); i++ {
		if src[i] == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	p := hcl.Pos{Line: line, Column: col, Byte: byteOff}
	return &hcl.Range{Filename: path, Start: p, End: p}
}
