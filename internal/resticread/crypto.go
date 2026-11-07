// Package resticread reads restic repositories over restic's REST protocol:
// it lists snapshots, walks their folders and streams file contents. It
// only reads, and it decrypts on this side, so it can run in a browser
// (compiled to WebAssembly) with the server only ever handing out
// encrypted data.
//
// The format is restic's, as described in its design document:
// https://restic.readthedocs.io/en/stable/100_references.html
package resticread

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/subtle"
	"encoding/json"
	"errors"

	"golang.org/x/crypto/poly1305"
	"golang.org/x/crypto/scrypt"
)

const (
	ivSize  = 16
	macSize = 16
)

// key is restic's key: AES-256-CTR for encryption, Poly1305-AES for
// authentication.
type key struct {
	encrypt []byte // 32 bytes
	macK    []byte // 16 bytes, AES key for the Poly1305 nonce
	macR    []byte // 16 bytes, Poly1305 r
}

var errMAC = errors.New("data doesn't check out: wrong password or damaged repo")

// open checks and decrypts IV || ciphertext || MAC.
func (k *key) open(data []byte) ([]byte, error) {
	if len(data) < ivSize+macSize {
		return nil, errors.New("encrypted data too short")
	}
	iv := data[:ivSize]
	ct := data[ivSize : len(data)-macSize]
	mac := data[len(data)-macSize:]
	if subtle.ConstantTimeCompare(k.mac(iv, ct), mac) != 1 {
		return nil, errMAC
	}
	block, err := aes.NewCipher(k.encrypt)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(ct))
	cipher.NewCTR(block, iv).XORKeyStream(out, ct)
	return out, nil
}

// mac is Poly1305-AES: the one-time key is r || AES-128_k(nonce).
func (k *key) mac(nonce, msg []byte) []byte {
	var pk [32]byte
	copy(pk[:16], k.macR)
	mask := [16]byte{0xff, 0xff, 0xff, 0x0f, 0xfc, 0xff, 0xff, 0x0f, 0xfc, 0xff, 0xff, 0x0f, 0xfc, 0xff, 0xff, 0x0f}
	for i := range mask {
		pk[i] &= mask[i]
	}
	block, err := aes.NewCipher(k.macK)
	if err != nil {
		panic(err)
	}
	block.Encrypt(pk[16:], nonce)
	var out [16]byte
	poly1305.Sum(&out, msg, &pk)
	return out[:]
}

// keyFile is a file in the repo's keys/ folder.
type keyFile struct {
	KDF  string `json:"kdf"`
	N    int    `json:"N"`
	R    int    `json:"r"`
	P    int    `json:"p"`
	Salt []byte `json:"salt"`
	Data []byte `json:"data"`
}

// masterKey is what a key file holds, once unlocked.
type masterKey struct {
	MAC struct {
		K []byte `json:"k"`
		R []byte `json:"r"`
	} `json:"mac"`
	Encrypt []byte `json:"encrypt"`
}

// unlockKeyFile derives the user key from the password and opens the
// master key with it.
func unlockKeyFile(raw []byte, password string) (*key, error) {
	var kf keyFile
	if err := json.Unmarshal(raw, &kf); err != nil {
		return nil, err
	}
	if kf.KDF != "scrypt" {
		return nil, errors.New("unknown key derivation " + kf.KDF)
	}
	// Refuse silly parameters rather than hang the browser.
	if kf.N <= 1 || kf.N > 1<<22 || kf.R <= 0 || kf.R > 64 || kf.P <= 0 || kf.P > 16 {
		return nil, errors.New("key file has unusual scrypt settings")
	}
	b, err := scrypt.Key([]byte(password), kf.Salt, kf.N, kf.R, kf.P, 64)
	if err != nil {
		return nil, err
	}
	user := &key{encrypt: b[:32], macK: b[32:48], macR: b[48:64]}
	plain, err := user.open(kf.Data)
	if err != nil {
		return nil, err
	}
	var mk masterKey
	if err := json.Unmarshal(plain, &mk); err != nil {
		return nil, err
	}
	if len(mk.Encrypt) != 32 || len(mk.MAC.K) != 16 || len(mk.MAC.R) != 16 {
		return nil, errors.New("master key has the wrong size")
	}
	return &key{encrypt: mk.Encrypt, macK: mk.MAC.K, macR: mk.MAC.R}, nil
}
