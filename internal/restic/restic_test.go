package restic

import (
	"strings"
	"testing"
)

func TestParseBackupOutput(t *testing.T) {
	out := strings.Join([]string{
		`{"message_type":"status","percent_done":0.5,"total_files":10,"files_done":5,"total_bytes":4096,"bytes_done":2048,"seconds_remaining":7}`,
		`not json`,
		`{"message_type":"summary","files_new":3,"files_changed":1,"data_added":2048,"total_files_processed":4,"snapshot_id":"abc123"}`,
	}, "\n")
	var errs strings.Builder
	var progress []BackupProgress
	sum, err := parseBackupOutput(strings.NewReader(out+"\n"+`{"message_type":"error","error":{"message":"permission denied"},"during":"archival","item":"/home/a/secret"}`), &errs,
		func(p BackupProgress) { progress = append(progress, p) })
	if err != nil {
		t.Fatal(err)
	}
	if sum == nil || sum.FilesNew != 3 || sum.FilesChanged != 1 || sum.DataAdded != 2048 || sum.SnapshotID != "abc123" {
		t.Fatalf("got %+v", sum)
	}
	if len(progress) != 1 || progress[0] != (BackupProgress{Percent: 0.5, TotalFiles: 10, FilesDone: 5, TotalBytes: 4096, BytesDone: 2048, SecondsRemaining: 7}) {
		t.Fatalf("progress %+v", progress)
	}
	if errs.String() != "/home/a/secret: permission denied\n" {
		t.Fatalf("errors %q", errs.String())
	}
}

func TestMessages(t *testing.T) {
	got := Messages("\n{\"message_type\":\"exit_error\",\"code\":1,\"message\":\"Fatal: repository is locked\"}\nplain line\n")
	if len(got) != 2 || got[0] != "Fatal: repository is locked" || got[1] != "plain line" {
		t.Fatalf("%q", got)
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
