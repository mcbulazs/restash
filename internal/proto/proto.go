// Package proto has the messages exchanged between agent and server.
package proto

import (
	"errors"
	"strconv"
	"time"

	"github.com/mcbulazs/restash/internal/schedule"
)

// JoinRequest is sent by a new PC with the code from the website.
type JoinRequest struct {
	Code    string `json:"code"`
	Name    string `json:"name"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	Version string `json:"version"`
}

// JoinResponse gives the PC its id and secret token.
type JoinResponse struct {
	PCID  string `json:"pcId"`
	Token string `json:"token"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

// Account is what an agent needs to unlock the account key with the
// owner's password. Everything in it is locked or public.
type Account struct {
	Email         string `json:"email"`
	Name          string `json:"name"`
	KDFSalt       string `json:"kdfSalt"`
	KDFIterations int    `json:"kdfIterations"`
	AccountKey    string `json:"accountKey"`
	PCKey         string `json:"pcKey,omitempty"`
}

// SetKey stores the PC's locked restic key.
type SetKey struct {
	PCKey string `json:"pcKey"`
}

// BackupReport is sent after each backup run. Detail is locked with the
// entry key: error messages can mention file names.
type BackupReport struct {
	Entry      string    `json:"entry"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
	OK         bool      `json:"ok"`
	Files      int64     `json:"files"`
	Bytes      int64     `json:"bytes"`
	Added      int64     `json:"added"`
	Snapshot   string    `json:"snapshot"`
	Detail     string    `json:"detail,omitempty"`
}

// Entry is one folder rule set as the server stores it: the schedule in the
// open, everything else locked with the entry key.
type Entry struct {
	ID        string            `json:"id"`
	Schedule  schedule.Schedule `json:"schedule"`
	Config    string            `json:"config"` // locked EntryConfig
	UpdatedAt time.Time         `json:"updatedAt"`
}

// EntryConfig is the locked part of an entry.
type EntryConfig struct {
	Name      string    `json:"name"`
	Paths     []string  `json:"paths"`
	Skips     []string  `json:"skips"`
	Retention Retention `json:"retention"`
}

// Retention thins out old backups step by step. Zero means the step is off.
type Retention struct {
	Last         int `json:"last"`          // always keep the last N backups
	AllDays      int `json:"allDays"`       // keep every backup for N days
	DailyDays    int `json:"dailyDays"`     // then one a day for N days
	WeeklyWeeks  int `json:"weeklyWeeks"`   // then one a week for N weeks
	MonthlyMonth int `json:"monthlyMonths"` // then one a month for N months
	YearlyYears  int `json:"yearlyYears"`   // then one a year for N years
}

// Validate makes sure retention keeps something.
func (r Retention) Validate() error {
	for _, n := range []int{r.Last, r.AllDays, r.DailyDays, r.WeeklyWeeks, r.MonthlyMonth, r.YearlyYears} {
		if n < 0 || n > 10000 {
			return errors.New("retention numbers must be between 0 and 10000")
		}
	}
	if r.Last+r.AllDays+r.DailyDays+r.WeeklyWeeks+r.MonthlyMonth+r.YearlyYears == 0 {
		return errors.New("retention would keep nothing")
	}
	return nil
}

// ForgetArgs are restic's flags for this retention.
func (r Retention) ForgetArgs() []string {
	var a []string
	add := func(flag string, n int, unit string) {
		if n > 0 {
			a = append(a, flag+"="+strconv.Itoa(n)+unit)
		}
	}
	add("--keep-last", r.Last, "")
	add("--keep-within", r.AllDays, "d")
	add("--keep-within-daily", r.DailyDays, "d")
	add("--keep-within-weekly", r.WeeklyWeeks*7, "d")
	add("--keep-within-monthly", r.MonthlyMonth, "m")
	add("--keep-within-yearly", r.YearlyYears, "y")
	return a
}

// BrowseRequest asks a PC for the folders inside Path ("" for the top).
type BrowseRequest struct {
	Path string `json:"path"`
}

// BrowseResult lists folders on the PC. Both travel locked.
type BrowseResult struct {
	Path   string   `json:"path"`   // the folder listed ("" for the top)
	Parent string   `json:"parent"` // "" at the top
	Dirs   []string `json:"dirs"`   // full paths of the folders inside
	Home   string   `json:"home"`   // the user's home folder, as a shortcut
	Sep    string   `json:"sep"`
	Error  string   `json:"error,omitempty"`
}

// Message is sent either way over the agent's websocket.
type Message struct {
	Type string `json:"type"`
	// hello (agent → server)
	Version  string `json:"version,omitempty"`
	Timezone string `json:"timezone,omitempty"`
	// run (server → agent): an entry id, or "" for all
	Entry string `json:"entry,omitempty"`
	// browse (server → agent) and browse-result (agent → server)
	ID   string `json:"id,omitempty"`
	Data string `json:"data,omitempty"` // locked BrowseRequest / BrowseResult
}

// Message types.
const (
	TypeHello        = "hello"
	TypeEntries      = "entries" // entries changed: fetch them again
	TypeRun          = "run"     // back up now
	TypeBrowse       = "browse"
	TypeBrowseResult = "browse-result"
)
