// Package vault holds the key handling shared by the agent and the browser
// (web/src/vault.ts does the same steps with WebCrypto; both are checked
// against testdata/vectors.json).
//
// A person's password is stretched with PBKDF2 into a master key, which is
// split with HKDF into:
//   - an auth key, sent to the server to log in (the server stores its hash);
//   - a wrap key, which never leaves the device and unlocks the account key.
//
// The account key is random and locks each PC's restic key. The recovery code
// gives a second auth/wrap pair, so the account key can be unlocked without
// the password. The server only ever holds locked keys.
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
)

// Iterations is the PBKDF2-SHA256 work factor for new passwords. It is
// stored per account so it can be raised later.
const Iterations = 600_000

const keyLen = 32

// Keys is what a password or recovery code turns into.
type Keys struct {
	Auth []byte // proves who you are to the server
	Wrap []byte // unlocks the account key; stays on the device
}

func split(master []byte) Keys {
	auth, err := hkdf.Key(sha256.New, master, nil, "restash auth v1", keyLen)
	if err != nil {
		panic(err)
	}
	wrap, err := hkdf.Key(sha256.New, master, nil, "restash wrap v1", keyLen)
	if err != nil {
		panic(err)
	}
	return Keys{Auth: auth, Wrap: wrap}
}

// PasswordKeys derives the keys for a password.
func PasswordKeys(password string, salt []byte, iterations int) (Keys, error) {
	if iterations < 1 {
		return Keys{}, errors.New("bad iteration count")
	}
	master, err := pbkdf2.Key(sha256.New, password, salt, iterations, keyLen)
	if err != nil {
		return Keys{}, err
	}
	return split(master), nil
}

// Recovery codes use Crockford's base32: 24 characters, 120 bits.
const recoveryAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewRecoveryCode makes a code like H3VD-Q8LN-2WKT-M7RC-9BXF-J5PZ.
func NewRecoveryCode() string {
	b := make([]byte, 24)
	rand.Read(b)
	var sb strings.Builder
	for i, c := range b {
		if i > 0 && i%4 == 0 {
			sb.WriteByte('-')
		}
		sb.WriteByte(recoveryAlphabet[c&31])
	}
	return sb.String()
}

// NormalizeRecoveryCode drops spaces and dashes, uppercases, and fixes the
// letters people mix up with digits.
func NormalizeRecoveryCode(code string) (string, error) {
	var sb strings.Builder
	for _, r := range strings.ToUpper(code) {
		switch r {
		case ' ', '-', '\t':
			continue
		case 'O':
			r = '0'
		case 'I', 'L':
			r = '1'
		}
		if !strings.ContainsRune(recoveryAlphabet, r) {
			return "", errors.New("recovery code has characters that don't belong in it")
		}
		sb.WriteRune(r)
	}
	if sb.Len() != 24 {
		return "", errors.New("recovery code should be 24 characters")
	}
	return sb.String(), nil
}

// RecoveryKeys derives the keys for a recovery code. The code is random, so
// no slow stretching is needed.
func RecoveryKeys(code string) (Keys, error) {
	n, err := NormalizeRecoveryCode(code)
	if err != nil {
		return Keys{}, err
	}
	master, err := hkdf.Key(sha256.New, []byte(n), nil, "restash recovery v1", keyLen)
	if err != nil {
		return Keys{}, err
	}
	return split(master), nil
}

// NewKey makes a random 256-bit key.
func NewKey() []byte {
	b := make([]byte, keyLen)
	rand.Read(b)
	return b
}

// NewSalt makes a random salt for PBKDF2.
func NewSalt() []byte {
	b := make([]byte, 16)
	rand.Read(b)
	return b
}

// Labels bind a locked value to what it is, so the server can't swap one
// locked value for another.
const LabelAccountKey = "restash account key"

// LabelPCKey is the label for a PC's restic key.
func LabelPCKey(pcID string) string { return "restash pc key " + pcID }

// Seal locks plaintext with key (AES-256-GCM). The label must match on Open.
func Seal(key, plaintext []byte, label string) (string, error) {
	nonce := make([]byte, 12)
	rand.Read(nonce)
	return sealWithNonce(key, nonce, plaintext, label)
}

func sealWithNonce(key, nonce, plaintext []byte, label string) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	out := gcm.Seal(append([]byte{}, nonce...), nonce, plaintext, []byte(label))
	return base64.StdEncoding.EncodeToString(out), nil
}

// ErrLocked means the key or label didn't match, e.g. a wrong password.
var ErrLocked = errors.New("could not unlock: wrong password or damaged data")

// Open unlocks a value made by Seal.
func Open(key []byte, sealed, label string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil || len(raw) < 12+16 {
		return nil, ErrLocked
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	out, err := gcm.Open(nil, raw[:12], raw[12:], []byte(label))
	if err != nil {
		return nil, ErrLocked
	}
	return out, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != keyLen {
		return nil, errors.New("key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// HashAuth is what the server stores for an auth key. Auth keys are random
// 256-bit values, so a plain hash is enough.
func HashAuth(auth []byte) string {
	sum := sha256.Sum256(auth)
	return hex.EncodeToString(sum[:])
}

// CheckAuth compares an auth key with a stored hash in constant time.
func CheckAuth(auth []byte, hash string) bool {
	return subtle.ConstantTimeCompare([]byte(HashAuth(auth)), []byte(hash)) == 1
}

// EntryKey is the key that locks a PC's entry settings (names, folders,
// skips) and error messages. It is derived from the PC's key, so only the
// PC and its owner can read them.
func EntryKey(pcKey []byte) []byte {
	k, err := hkdf.Key(sha256.New, pcKey, nil, "restash entries v1", keyLen)
	if err != nil {
		panic(err)
	}
	return k
}

// LabelEntry is the label for an entry's settings.
func LabelEntry(pcID, entryID string) string { return "restash entry " + pcID + " " + entryID }

// LabelRunDetail is the label for the details of one backup run.
func LabelRunDetail(pcID string) string { return "restash run detail " + pcID }

// LabelBrowse is the label for folder listings between browser and PC.
func LabelBrowse(pcID string) string { return "restash browse " + pcID }

// PCKeyFromPassword reverses PCPassword.
func PCKeyFromPassword(p string) ([]byte, error) {
	b, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil || len(b) != keyLen {
		return nil, errors.New("bad PC key")
	}
	return b, nil
}

// PCPassword turns a PC's raw key into the password restic is given.
func PCPassword(key []byte) string {
	return base64.RawURLEncoding.EncodeToString(key)
}
