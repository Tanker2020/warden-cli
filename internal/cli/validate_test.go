package cli

import "testing"

const validRule = `if condition("event.source == 'aws.cloudwatch'") guard(approval()) { aws_policy "iam_deny" "r" { target_arn = "arn:aws:iam::1:role/x" } }`

func TestValidateSource(t *testing.T) {
	if _, err := validateSource("ok.sac", []byte(validRule), false); err != nil {
		t.Fatalf("valid rule: %v", err)
	}
	if _, err := validateSource("bad.sac", []byte(`if condition( { oops`), false); err == nil {
		t.Fatal("want parse error")
	}
}

// TestValidateWarnsOnFlapProneCondition pins that the embedded lint flags a
// metric-threshold condition with no guard (the hysteresis heuristic).
func TestValidateWarnsOnFlapProneCondition(t *testing.T) {
	src := `if condition("event.detail.value > 90") { aws_policy "iam_deny" "r" { target_arn = "arn:aws:iam::1:role/x" } }`
	warnings, err := validateSource("flappy.sac", []byte(src), false)
	if err != nil {
		t.Fatal(err)
	}
	if warnings == 0 {
		t.Fatal("want at least one lint warning for an unguarded metric threshold")
	}
}
