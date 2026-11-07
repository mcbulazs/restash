package agent

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/mcbulazs/restash/internal/restic"
	"github.com/mcbulazs/restash/internal/vault"
)

// Unlock opens the account key with the password, then gets this PC's restic
// key: the existing one if the server has it, or a new one that it locks
// with the account key and stores. It also creates the repo if needed.
// The password and unlocked keys never leave this PC.
func Unlock(ctx context.Context, cfg *Config, password string, r *restic.Restic) error {
	c := NewClient(cfg.Server, cfg.PCID, cfg.Token)
	acc, err := c.Account(ctx)
	if err != nil {
		return err
	}
	salt, err := base64.StdEncoding.DecodeString(acc.KDFSalt)
	if err != nil {
		return errors.New("the server sent a bad salt")
	}
	keys, err := vault.PasswordKeys(password, salt, acc.KDFIterations)
	if err != nil {
		return err
	}
	accountKey, err := vault.Open(keys.Wrap, acc.AccountKey, vault.LabelAccountKey)
	if errors.Is(err, vault.ErrLocked) {
		return ErrWrongPassword
	}
	if err != nil {
		return err
	}

	label := vault.LabelPCKey(cfg.PCID)
	var pcKey []byte
	if acc.PCKey != "" {
		if pcKey, err = vault.Open(accountKey, acc.PCKey, label); err != nil {
			return fmt.Errorf("this PC's key on the server doesn't open with your account: %w", err)
		}
	} else {
		pcKey = vault.NewKey()
		sealed, err := vault.Seal(accountKey, pcKey, label)
		if err != nil {
			return err
		}
		if err := c.SetKey(ctx, sealed); err != nil {
			return fmt.Errorf("storing this PC's key: %w", err)
		}
	}

	cfg.RepoKey = vault.PCPassword(pcKey)
	r.Repo = cfg.Repo()
	exists, err := r.Exists(ctx)
	if err != nil {
		return err
	}
	if !exists {
		// restic's own note about not losing the password would only confuse:
		// people never see this key, their account password unlocks it.
		out := r.Stdout
		r.Stdout = nil
		err := r.Init(ctx)
		r.Stdout = out
		if err != nil {
			return fmt.Errorf("creating the backup repo: %w", err)
		}
	}
	return nil
}

// ErrWrongPassword means the password didn't unlock the account key.
var ErrWrongPassword = errors.New("wrong password")
