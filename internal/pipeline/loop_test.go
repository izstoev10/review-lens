package pipeline

import (
	"testing"

	"github.com/izstoev10/review-lens/internal/ci"
	"github.com/izstoev10/review-lens/internal/findings"
)

func TestDecideNext(t *testing.T) {
	cases := []struct {
		name             string
		autoFix, askUser int
		status           ci.Status
		want             loopAction
	}{
		{"auto-fix wins over everything", 2, 3, ci.Failure, actApplyFixes},
		{"ask-user escalates when nothing to auto-fix", 0, 1, ci.Success, actEscalate},
		{"no findings + green = done", 0, 0, ci.Success, actDone},
		{"no findings + red = fix CI", 0, 0, ci.Failure, actFixCI},
		{"no findings + pending = wait", 0, 0, ci.Pending, actWaitCI},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := decideNext(c.autoFix, c.askUser, c.status); got != c.want {
				t.Errorf("decideNext(%d,%d,%v) = %v, want %v", c.autoFix, c.askUser, c.status, got, c.want)
			}
		})
	}
}

func TestPartition(t *testing.T) {
	items := []findings.Finding{
		{Action: findings.AutoFix},
		{Action: findings.AskUser},
		{Action: findings.NoOp},
		{Action: ""}, // unknown → treated as ask-user (needs a human)
	}
	autoFix, askUser, noOp := partition(items)
	if len(autoFix) != 1 || len(askUser) != 2 || len(noOp) != 1 {
		t.Errorf("partition sizes = (%d,%d,%d), want (1,2,1)", len(autoFix), len(askUser), len(noOp))
	}
}
