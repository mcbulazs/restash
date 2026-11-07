// Package proto has the messages exchanged between agent and server.
package proto

import "time"

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

// BackupReport is sent after each backup run. It holds no names or paths.
type BackupReport struct {
	Entry      string    `json:"entry"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
	OK         bool      `json:"ok"`
	Files      int64     `json:"files"`
	Bytes      int64     `json:"bytes"`
	Added      int64     `json:"added"`
	Snapshot   string    `json:"snapshot"`
}

// Message is sent either way over the agent's websocket.
type Message struct {
	Type string `json:"type"`
	// hello (agent → server)
	Version string `json:"version,omitempty"`
}

// Message types.
const (
	TypeHello = "hello"
)
