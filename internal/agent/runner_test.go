package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mcbulazs/restash/internal/proto"
	"github.com/mcbulazs/restash/internal/vault"
)

func TestState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := LoadState(path)
	if err != nil || st.LastRun == nil {
		t.Fatal(st, err)
	}
	now := time.Now().Truncate(time.Second)
	st.LastRun["a"] = now
	st.PruneNeeded = true
	if err := st.Save(path); err != nil {
		t.Fatal(err)
	}
	got, _ := LoadState(path)
	if !got.LastRun["a"].Equal(now) || !got.PruneNeeded {
		t.Fatalf("%+v", got)
	}
	os.WriteFile(path, []byte("garbage"), 0o600)
	if got, err := LoadState(path); err != nil || len(got.LastRun) != 0 {
		t.Fatal("damaged state should start over")
	}
}

func TestRetentionArgs(t *testing.T) {
	r := proto.Retention{AllDays: 2, DailyDays: 30, WeeklyWeeks: 4, MonthlyMonth: 24}
	got := strings.Join(r.ForgetArgs(), " ")
	if got != "--keep-within=2d --keep-within-daily=30d --keep-within-weekly=28d --keep-within-monthly=24m" {
		t.Fatal(got)
	}
	if (proto.Retention{}).Validate() == nil {
		t.Fatal("empty retention accepted")
	}
}

func TestListFolders(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"b", "A", ".hidden"} {
		os.Mkdir(filepath.Join(dir, d), 0o700)
	}
	os.WriteFile(filepath.Join(dir, "file.txt"), nil, 0o600)
	res := ListFolders(dir, "")
	if res.Error != "" || len(res.Dirs) != 2 || filepath.Base(res.Dirs[0]) != "A" || res.Parent != filepath.Dir(dir) {
		t.Fatalf("%+v", res)
	}
	if top := ListFolders("", ""); len(top.Dirs) == 0 {
		t.Fatal("no top level")
	}
	if bad := ListFolders(filepath.Join(dir, "missing"), ""); bad.Error == "" {
		t.Fatal("missing folder listed")
	}
}

func TestAnswerBrowse(t *testing.T) {
	key := vault.EntryKey(vault.NewKey())
	req, _ := json.Marshal(proto.BrowseRequest{Path: t.TempDir()})
	sealed, _ := vault.Seal(key, req, vault.LabelBrowse("pc1"))
	out, err := AnswerBrowse(key, "pc1", "", sealed)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := vault.Open(key, out, vault.LabelBrowse("pc1"))
	if err != nil || !strings.Contains(string(plain), `"dirs":[]`) {
		t.Fatalf("%s %v", plain, err)
	}
	// A request locked for another PC is refused.
	other, _ := vault.Seal(key, req, vault.LabelBrowse("pc2"))
	if _, err := AnswerBrowse(key, "pc1", "", other); err == nil {
		t.Fatal("answered a request for another PC")
	}
}
