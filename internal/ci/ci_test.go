package ci

import (
	"testing"

	"github.com/izstoev10/review-lens/internal/gh"
)

func TestClassifyRollup(t *testing.T) {
	cases := []struct {
		name string
		runs []gh.CheckRun
		want Status
	}{
		{"empty = success", nil, Success},
		{"all completed success", []gh.CheckRun{
			{Status: "COMPLETED", Conclusion: "SUCCESS"},
			{Status: "COMPLETED", Conclusion: "SUCCESS"},
		}, Success},
		{"one failure wins", []gh.CheckRun{
			{Status: "COMPLETED", Conclusion: "SUCCESS"},
			{Status: "COMPLETED", Conclusion: "FAILURE"},
		}, Failure},
		{"one in progress = pending", []gh.CheckRun{
			{Status: "COMPLETED", Conclusion: "SUCCESS"},
			{Status: "IN_PROGRESS"},
		}, Pending},
		{"failure beats pending", []gh.CheckRun{
			{Status: "IN_PROGRESS"},
			{Status: "COMPLETED", Conclusion: "FAILURE"},
		}, Failure},
		{"legacy commit status pending", []gh.CheckRun{
			{State: "PENDING"},
		}, Pending},
		{"legacy commit status failure", []gh.CheckRun{
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

// Query end to end through a fake client: the rollup JSON decodes and
// classifies without a real gh anywhere.
func TestQueryClassifiesThroughTheClient(t *testing.T) {
	c := gh.Client{Exec: func(args ...string) ([]byte, error) {
		return []byte(`{"statusCheckRollup":[
			{"name":"build","status":"COMPLETED","conclusion":"SUCCESS"},
			{"name":"test","status":"COMPLETED","conclusion":"FAILURE"}]}`), nil
	}}

	status, failing, err := Query(c, "7")
	if err != nil {
		t.Fatal(err)
	}
	if status != Failure {
		t.Errorf("status = %v, want Failure", status)
	}
	if len(failing) != 1 || failing[0] != "test" {
		t.Errorf("failing = %v, want [test]", failing)
	}
}
