package agent

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Updates come only from the project's GitHub releases, never from the
// restash server, so whoever runs a server can't push agents a different
// build.
const (
	ReleasesAPI      = "https://api.github.com/repos/mcbulazs/restash/releases/latest"
	ReleasesDownload = "https://github.com/mcbulazs/restash/releases/download/"
)

// Updater replaces the agent's own binary with the newest release.
type Updater struct {
	Current  string // this build's version, e.g. v1.2.0
	Exe      string // the binary to replace
	API      string // ReleasesAPI
	Download string // ReleasesDownload
	Client   *http.Client
}

// AssetName is the release file for this OS and CPU.
func AssetName() string {
	name := "restash-agent-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// ParseVersion reads "v1.2.3" (a leading v optional). ok is false for
// anything else, e.g. development builds.
func ParseVersion(v string) (out [3]int, ok bool) {
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// Newer says whether version a is newer than b.
func Newer(a, b string) bool {
	va, ok1 := ParseVersion(a)
	vb, ok2 := ParseVersion(b)
	if !ok1 || !ok2 {
		return false
	}
	for i := range va {
		if va[i] != vb[i] {
			return va[i] > vb[i]
		}
	}
	return false
}

func (u *Updater) client() *http.Client {
	if u.Client != nil {
		return u.Client
	}
	return &http.Client{Timeout: 10 * time.Minute}
}

func (u *Updater) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "restash-agent/"+u.Current)
	resp, err := u.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return resp, nil
}

// Latest is the newest release's version.
func (u *Updater) Latest(ctx context.Context) (string, error) {
	resp, err := u.get(ctx, u.API)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var rel struct {
		Tag string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return "", err
	}
	return rel.Tag, nil
}

// Check returns the newer version to update to, or "" if there's none.
// Development builds never update.
func (u *Updater) Check(ctx context.Context) (string, error) {
	if _, ok := ParseVersion(u.Current); !ok {
		return "", nil
	}
	latest, err := u.Latest(ctx)
	if err != nil {
		return "", err
	}
	if !Newer(latest, u.Current) {
		return "", nil
	}
	return latest, nil
}

// Apply downloads version, checks it against the release's SHA256SUMS and
// puts it in place of Exe. The new binary runs after a restart.
func (u *Updater) Apply(ctx context.Context, version string) error {
	base := strings.TrimSuffix(u.Download, "/") + "/" + version + "/"
	asset := AssetName()
	want, err := u.sum(ctx, base+"SHA256SUMS", asset)
	if err != nil {
		return err
	}
	resp, err := u.get(ctx, base+asset)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	tmp := u.Exe + ".new"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, 512<<20))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && hex.EncodeToString(h.Sum(nil)) != want {
		err = errors.New("the download doesn't match the release's SHA256SUMS")
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return replaceFile(tmp, u.Exe)
}

// sum finds asset's hash in a SHA256SUMS file.
func (u *Updater) sum(ctx context.Context, url, asset string) (string, error) {
	resp, err := u.get(ctx, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 1<<20))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == asset && len(f[0]) == 64 {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("%s isn't in the release's SHA256SUMS", asset)
}

// replaceFile moves src over dst. A running binary can't be overwritten on
// Windows but can be renamed, so dst moves aside to dst.old first.
func replaceFile(src, dst string) error {
	old := dst + ".old"
	os.Remove(old)
	if err := os.Rename(dst, old); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		os.Rename(old, dst)
		return err
	}
	return nil
}

// ErrUpdated means the binary was replaced and the agent should exit so the
// service manager starts the new one.
var ErrUpdated = errors.New("updated; restarting")

// KeepUpdated checks for a new release a minute after starting and then
// every 6 hours. When one is installed it returns ErrUpdated, waiting until
// busy says no backup or restore is running.
func (u *Updater) KeepUpdated(ctx context.Context, busy func() bool, log func(msg string, args ...any)) error {
	// Clean up after the last update.
	os.Remove(u.Exe + ".old")
	wait := time.Minute
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		wait = 6 * time.Hour
		v, err := u.Check(ctx)
		if err != nil {
			log("couldn't check for updates", "err", err)
			continue
		}
		if v == "" {
			continue
		}
		for busy() {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Minute):
			}
		}
		log("updating", "from", u.Current, "to", v)
		if err := u.Apply(ctx, v); err != nil {
			log("update failed", "err", err)
			continue
		}
		return ErrUpdated
	}
}
