package review

import (
	"reflect"
	"testing"
)

// TestLauncherEnvSetsWhatItKnows: the session's launcher marker carries
// the brief's values in the contract's order, and a value the brief lacks
// leaves its variable absent rather than empty (WU-069).
func TestLauncherEnvSetsWhatItKnows(t *testing.T) {
	b := Brief{ID: "rv_1", RunID: "run_1", ChangeID: "c_1", RepoID: "r_1", RepoURL: "https://github.com/a/b.git"}
	want := []string{"PLUMBIX_LAUNCHER=review", "PLUMBIX_REPOSITORY=https://github.com/a/b.git", "PLUMBIX_REPO_ID=r_1", "PLUMBIX_CHANGE=c_1", "PLUMBIX_RUN=run_1", "PLUMBIX_REVIEW=rv_1"}
	if got := b.LauncherEnv(); !reflect.DeepEqual(got, want) {
		t.Errorf("env = %v, want %v", got, want)
	}
	b.RepoURL, b.RunID = "", ""
	want = []string{"PLUMBIX_LAUNCHER=review", "PLUMBIX_REPO_ID=r_1", "PLUMBIX_CHANGE=c_1", "PLUMBIX_REVIEW=rv_1"}
	if got := b.LauncherEnv(); !reflect.DeepEqual(got, want) {
		t.Errorf("env with gaps = %v, want %v", got, want)
	}
	if got := LauncherVars(LauncherValidator, map[string]string{EnvChange: ""}); !reflect.DeepEqual(got, []string{"PLUMBIX_LAUNCHER=validator"}) {
		t.Errorf("validator env with no change = %v", got)
	}
}
