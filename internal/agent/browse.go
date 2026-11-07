package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/mcbulazs/restash/internal/proto"
	"github.com/mcbulazs/restash/internal/vault"
)

// maxDirs caps one listing, so a huge folder can't flood the link.
const maxDirs = 2000

// ListFolders lists the folders inside path, or the top level ("" path):
// drives on Windows, / elsewhere.
func ListFolders(path string) proto.BrowseResult {
	res := proto.BrowseResult{Path: path, Sep: string(filepath.Separator)}
	if home, err := os.UserHomeDir(); err == nil {
		res.Home = home
	}
	if path == "" {
		res.Dirs = roots()
		return res
	}
	path = filepath.Clean(path)
	res.Path = path
	if parent := filepath.Dir(path); parent != path {
		res.Parent = parent
	}
	list, err := os.ReadDir(path)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Dirs = []string{}
	for _, d := range list {
		if strings.HasPrefix(d.Name(), ".") {
			continue
		}
		isDir := d.IsDir()
		if !isDir && d.Type()&os.ModeSymlink != 0 {
			if st, err := os.Stat(filepath.Join(path, d.Name())); err == nil && st.IsDir() {
				isDir = true
			}
		}
		if isDir {
			res.Dirs = append(res.Dirs, filepath.Join(path, d.Name()))
			if len(res.Dirs) >= maxDirs {
				break
			}
		}
	}
	sort.Slice(res.Dirs, func(i, j int) bool { return strings.ToLower(res.Dirs[i]) < strings.ToLower(res.Dirs[j]) })
	return res
}

func roots() []string {
	if runtime.GOOS != "windows" {
		return []string{"/"}
	}
	var out []string
	for c := 'A'; c <= 'Z'; c++ {
		d := string(c) + `:\`
		if _, err := os.Stat(d); err == nil {
			out = append(out, d)
		}
	}
	return out
}

// AnswerBrowse unlocks a folder listing request, lists, and locks the answer.
func AnswerBrowse(entryKey []byte, pcID, data string) (string, error) {
	label := vault.LabelBrowse(pcID)
	plain, err := vault.Open(entryKey, data, label)
	if err != nil {
		return "", err
	}
	var req proto.BrowseRequest
	if err := json.Unmarshal(plain, &req); err != nil {
		return "", err
	}
	out, err := json.Marshal(ListFolders(req.Path))
	if err != nil {
		return "", err
	}
	return vault.Seal(entryKey, out, label)
}
