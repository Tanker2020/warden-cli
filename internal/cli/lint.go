package cli

import (
	"fmt"
	"regexp"
	"strings"

	"sac/lang/parser"
)

// metricThreshold matches a comparison whose LHS path looks like a
// measurement (cpu, value, utilization, count, percent, memory, latency,
// rate) and whose RHS is numeric, e.g. `detail.value > 90`. This is the
// shape of condition most prone to threshold-flapping (FEATURES.md #2,
// case 1).
var metricThreshold = regexp.MustCompile(`(?i)[\w.]*(cpu|value|utilization|count|percent|memory|latency|rate)[\w.]*\s*(>=|<=|>|<)\s*-?\d+(\.\d+)?`)

// lintGuards runs the flap-prone-condition heuristics described in
// FEATURES.md's "Linter: suggest a guard when a condition is flap-prone"
// section against every if-block in f, returning one warning string per
// finding. serverMode additionally enables the generic no-guard-at-all
// nudge, since repeated firing is cheapest to trigger by accident under
// `sac serve`.
func lintGuards(f *parser.File, serverMode bool) []string {
	var warnings []string

	for i := range f.IfBlocks {
		ifb := &f.IfBlocks[i]
		if metricThreshold.MatchString(ifb.Condition) && !hasGuard(ifb.GuardSpec, "sustained") {
			warnings = append(warnings, fmt.Sprintf(
				"line %d: condition %q compares a metric against a threshold with no guard — "+
					"this is prone to flapping near the boundary; consider `guard(sustained(\"3m\"))`",
				ifb.Line, ifb.Condition))
		}
	}

	resourceOwners := map[string][]int{} // resource identifier -> if-block indexes that touch it
	for i := range f.IfBlocks {
		for _, b := range f.IfBlocks[i].Blocks {
			id := b.Identifier()
			resourceOwners[id] = append(resourceOwners[id], i)
		}
	}
	for id, owners := range resourceOwners {
		if len(owners) < 2 {
			continue
		}
		for _, i := range owners {
			ifb := &f.IfBlocks[i]
			if hasGuard(ifb.GuardSpec, "resource_cooldown") {
				continue
			}
			others := otherConditions(f, owners, i)
			warnings = append(warnings, fmt.Sprintf(
				"line %d: condition %q acts on %q, which is also acted on by %s with no `resource_cooldown` guard — "+
					"opposing conditions racing on the same resource can flap it back and forth; consider `guard(resource_cooldown(\"5m\"))`",
				ifb.Line, ifb.Condition, id, others))
		}
	}

	if serverMode {
		for i := range f.IfBlocks {
			ifb := &f.IfBlocks[i]
			if strings.TrimSpace(ifb.GuardSpec) == "" {
				warnings = append(warnings, fmt.Sprintf(
					"line %d: condition %q has no guard clause — under `sac serve` a repeatedly-matching event "+
						"will redispatch every time; see the guard list (debounce, rate_limit, sustained, resource_cooldown, dedup)",
					ifb.Line, ifb.Condition))
			}
		}
	}

	return warnings
}

func hasGuard(spec, name string) bool {
	return strings.Contains(spec, name+"(")
}

func otherConditions(f *parser.File, owners []int, exclude int) string {
	var parts []string
	for _, i := range owners {
		if i == exclude {
			continue
		}
		parts = append(parts, fmt.Sprintf("%q (line %d)", f.IfBlocks[i].Condition, f.IfBlocks[i].Line))
	}
	return strings.Join(parts, ", ")
}
