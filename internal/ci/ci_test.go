package ci

import "testing"

func TestClassifyRollup(t *testing.T) {
	cases := []struct {
		name string
		runs []checkRun
		want Status
	}{
		{"empty = success", nil, Success},
		{"all completed success", []checkRun{
			{Status: "COMPLETED", Conclusion: "SUCCESS"},
			{Status: "COMPLETED", Conclusion: "SUCCESS"},
		}, Success},
		{"one failure wins", []checkRun{
			{Status: "COMPLETED", Conclusion: "SUCCESS"},
			{Status: "COMPLETED", Conclusion: "FAILURE"},
		}, Failure},
		{"one in progress = pending", []checkRun{
			{Status: "COMPLETED", Conclusion: "SUCCESS"},
			{Status: "IN_PROGRESS"},
		}, Pending},
		{"failure beats pending", []checkRun{
			{Status: "IN_PROGRESS"},
			{Status: "COMPLETED", Conclusion: "FAILURE"},
		}, Failure},
		{"legacy commit status pending", []checkRun{
			{State: "PENDING"},
		}, Pending},
		{"legacy commit status failure", []checkRun{
			{State: "FAILURE"},
		}, Failure},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, _ := classifyRollup(c.runs); got != c.want {
				t.Errorf("classifyRollup = %v, want %v", got, c.want)
			}
		})
	}
}
