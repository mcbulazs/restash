package restic

import (
	"strings"
	"testing"
)

func TestParseBackupOutput(t *testing.T) {
	out := strings.Join([]string{
		`{"message_type":"status","percent_done":0.5}`,
		`not json`,
		`{"message_type":"summary","files_new":3,"files_changed":1,"data_added":2048,"total_files_processed":4,"snapshot_id":"abc123"}`,
	}, "\n")
	sum, err := parseBackupOutput(strings.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if sum == nil || sum.FilesNew != 3 || sum.FilesChanged != 1 || sum.DataAdded != 2048 || sum.SnapshotID != "abc123" {
		t.Fatalf("got %+v", sum)
	}
}

func TestEnvDropsUserResticVars(t *testing.T) {
	t.Setenv("RESTIC_REPOSITORY", "/somewhere/else")
	r := &Restic{Repo: Repo{URL: "https://example.com/repo/anna/", Username: "anna", Password: "p", Key: "k"}}
	var repos []string
	for _, kv := range r.env() {
		if strings.HasPrefix(kv, "RESTIC_REPOSITORY=") {
			repos = append(repos, kv)
		}
	}
	if len(repos) != 1 || repos[0] != "RESTIC_REPOSITORY=rest:https://example.com/repo/anna/" {
		t.Fatalf("got %v", repos)
	}
}
