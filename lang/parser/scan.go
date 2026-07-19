package parser

import (
	"github.com/hashicorp/hcl/v2"
)

type region struct {
	ifStart   int // byte offset of 'i' in if
	condition string
	guardSpec string // raw contents of an optional guard(...) clause
	bodyStart int    // first byte after {
	bodyEnd   int    // byte of }
	end       int    // first byte after }
}

func isWS(b byte) bool { return b == ' ' || b == '\t' || b == '\r' || b == '\n' }

// scanIfBlocks finds top-level `if condition("...") { }` regions, skipping
// string and comment content so braces inside them are ignored.
func scanIfBlocks(path string, src []byte) ([]region, hcl.Diagnostics) {
	var (
		out   []region
		diags hcl.Diagnostics
		i     int
		n     = len(src)
	)
	for i < n {
		switch {
		case src[i] == '"':
			i = skipString(src, i)
			continue
		case src[i] == '#':
			i = skipLine(src, i)
			continue
		case i+1 < n && src[i] == '/' && src[i+1] == '/':
			i = skipLine(src, i)
			continue
		case i+1 < n && src[i] == '/' && src[i+1] == '*':
			i = skipBlock(src, i)
			continue
		}
		if matchIf(src, i) {
			r, d := readIf(path, src, i)
			diags = append(diags, d...)
			if d.HasErrors() {
				return out, diags
			}
			out = append(out, r)
			i = r.end
			continue
		}
		i++
	}
	return out, diags
}

func matchIf(src []byte, i int) bool {
	// word boundary before "if"
	if i > 0 && !isWS(src[i-1]) && src[i-1] != '}' && src[i-1] != '{' {
		return false
	}
	if i+2 > len(src) || src[i] != 'i' || src[i+1] != 'f' {
		return false
	}
	j := i + 2
	if j >= len(src) || !isWS(src[j]) {
		return false
	}
	for j < len(src) && isWS(src[j]) {
		j++
	}
	const kw = "condition"
	if j+len(kw) > len(src) || string(src[j:j+len(kw)]) != kw {
		return false
	}
	j += len(kw)
	for j < len(src) && isWS(src[j]) {
		j++
	}
	return j < len(src) && src[j] == '('
}

func readIf(path string, src []byte, start int) (region, hcl.Diagnostics) {
	j := start + 2
	for j < len(src) && isWS(src[j]) {
		j++
	}
	j += len("condition")
	for j < len(src) && isWS(src[j]) {
		j++
	}
	j++ // (
	for j < len(src) && isWS(src[j]) {
		j++
	}
	if j >= len(src) || src[j] != '"' {
		return region{}, hcl.Diagnostics{{Severity: hcl.DiagError, Summary: "condition must be a string literal", Subject: posRange(path, src, j)}}
	}
	end := skipString(src, j)
	cond := string(src[j+1 : end-1])
	for end < len(src) && (isWS(src[end]) || src[end] == ')') {
		if src[end] == ')' {
			end++
			break
		}
		end++
	}
	for end < len(src) && isWS(src[end]) {
		end++
	}
	// optional `guard(...)` clause between condition() and the body.
	guardSpec := ""
	if isIdentChar := func(b byte) bool {
		return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
	}; end+5 <= len(src) && string(src[end:end+5]) == "guard" && (end+5 >= len(src) || !isIdentChar(src[end+5])) {
		k := end + 5
		for k < len(src) && isWS(src[k]) {
			k++
		}
		if k >= len(src) || src[k] != '(' {
			return region{}, hcl.Diagnostics{{Severity: hcl.DiagError, Summary: "expected ( after guard", Subject: posRange(path, src, k)}}
		}
		k++
		argsStart := k
		depth := 1
		for k < len(src) && depth > 0 {
			switch src[k] {
			case '"':
				k = skipString(src, k)
				continue
			case '(':
				depth++
			case ')':
				depth--
			}
			k++
		}
		if depth != 0 {
			return region{}, hcl.Diagnostics{{Severity: hcl.DiagError, Summary: "unterminated guard(...) clause", Subject: posRange(path, src, end)}}
		}
		guardSpec = string(src[argsStart : k-1])
		end = k
		for end < len(src) && isWS(src[end]) {
			end++
		}
	}
	if end >= len(src) || src[end] != '{' {
		return region{}, hcl.Diagnostics{{Severity: hcl.DiagError, Summary: "expected { after if condition()", Subject: posRange(path, src, end)}}
	}
	bodyStart := end + 1
	depth, k := 1, bodyStart
	for k < len(src) && depth > 0 {
		switch {
		case src[k] == '"':
			k = skipString(src, k)
			continue
		case src[k] == '#':
			k = skipLine(src, k)
			continue
		case k+1 < len(src) && src[k] == '/' && src[k+1] == '/':
			k = skipLine(src, k)
			continue
		case k+1 < len(src) && src[k] == '/' && src[k+1] == '*':
			k = skipBlock(src, k)
			continue
		case src[k] == '{':
			depth++
		case src[k] == '}':
			depth--
			if depth == 0 {
				return region{ifStart: start, condition: cond, guardSpec: guardSpec, bodyStart: bodyStart, bodyEnd: k, end: k + 1}, nil
			}
		}
		k++
	}
	return region{}, hcl.Diagnostics{{Severity: hcl.DiagError, Summary: "unterminated if block", Subject: posRange(path, src, start)}}
}

func skipString(src []byte, i int) int {
	i++
	for i < len(src) {
		if src[i] == '\\' {
			i += 2
			continue
		}
		if src[i] == '"' {
			return i + 1
		}
		i++
	}
	return i
}

func skipLine(src []byte, i int) int {
	for i < len(src) && src[i] != '\n' {
		i++
	}
	return i
}

func skipBlock(src []byte, i int) int {
	i += 2
	for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
		i++
	}
	return i + 2
}

// blankRegions replaces if-regions with whitespace, preserving newlines so the
// remaining HCL keeps original line numbers.
func blankRegions(src []byte, regions []region) []byte {
	out := append([]byte(nil), src...)
	for _, r := range regions {
		for i := r.ifStart; i < r.end && i < len(out); i++ {
			if out[i] != '\n' {
				out[i] = ' '
			}
		}
	}
	return out
}
