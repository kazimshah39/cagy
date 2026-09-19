package accounts

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/kazimshah39/cagy/internal/securestate"
)

var ErrCredentialUnavailable = errors.New("account credential is not available")

const (
	fileCredentialMaxBytes = 1 << 20
	fileCredentialPrefix   = "account-credential-"
	fileCredentialSuffix   = ".bin"
)

// FileCredentialVault stores agy account snapshots as private 0600 files. This
// is intentionally the default for cagy's personal-Mac workflow: it avoids
// macOS ACL/password prompts while keeping credentials out of catalog metadata.
// The agy canonical item remains owned by agy and is not migrated here.
type FileCredentialVault struct {
	StateDir string
}

func (v FileCredentialVault) fileName(accountID string) (string, error) {
	if len(accountID) != 64 {
		return "", errors.New("account ID must be a 64-character hex string")
	}
	if _, err := hex.DecodeString(accountID); err != nil {
		return "", errors.New("account ID must be hexadecimal")
	}
	return fileCredentialPrefix + accountID + fileCredentialSuffix, nil
}

func (v FileCredentialVault) Path(accountID string) (string, error) {
	name, err := v.fileName(accountID)
	if err != nil {
		return "", err
	}
	if v.StateDir == "" {
		return "", errors.New("credential state directory is unavailable")
	}
	return filepath.Join(v.StateDir, name), nil
}

func (v FileCredentialVault) Load(ctx context.Context, accountID string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := v.Path(accountID)
	if err != nil {
		return nil, err
	}
	data, exists, err := securestate.ReadFile(path, fileCredentialMaxBytes)
	if err != nil {
		return nil, fmt.Errorf("read account credential file: %w", err)
	}
	if !exists {
		return nil, ErrCredentialUnavailable
	}
	return data, nil
}

func (v FileCredentialVault) Save(ctx context.Context, accountID string, secret []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(secret) == 0 {
		return errors.New("account credential cannot be empty")
	}
	name, err := v.fileName(accountID)
	if err != nil {
		return err
	}
	if v.StateDir == "" {
		return errors.New("credential state directory is unavailable")
	}
	return securestate.WriteFile(v.StateDir, name, secret)
}

func (v FileCredentialVault) Delete(ctx context.Context, accountID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	name, err := v.fileName(accountID)
	if err != nil {
		return err
	}
	return securestate.RemoveFile(v.StateDir, name)
}

func (v FileCredentialVault) Exists(ctx context.Context, accountID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	path, err := v.Path(accountID)
	if err != nil {
		return false, err
	}
	_, exists, err := securestate.ReadFile(path, fileCredentialMaxBytes)
	return exists, err
}
