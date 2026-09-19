package app

import (
	"context"
	"fmt"
	"time"

	"github.com/kazimshah39/cagy/internal/accounts"
)

// storedAccountValidator validates and refreshes one cagy credential directly
// with Google. It never starts agy, opens a browser, or reads the canonical
// Keychain item.
type storedAccountValidator struct {
	identity  accounts.IdentityResolver
	refresher accounts.GoogleCredentialRefresher
	now       func() time.Time
}

func (v storedAccountValidator) Validate(ctx context.Context, credential []byte) (accounts.ValidationResult, error) {
	refreshed, err := v.refresher.Refresh(ctx, credential)
	if err != nil {
		return accounts.ValidationResult{}, err
	}
	defer accounts.Zero(refreshed)
	identity, err := v.identity.Resolve(ctx, refreshed)
	if err != nil {
		return accounts.ValidationResult{}, fmt.Errorf("verify Google account identity: %w", err)
	}
	now := time.Now().UTC()
	if v.now != nil {
		now = v.now().UTC()
	}
	return accounts.ValidationResult{
		Identity: identity,
		Quota: accounts.QuotaSnapshot{
			Class:      accounts.QuotaUnknown,
			Reason:     "quota is checked after agy starts",
			ObservedAt: now,
		},
		Credential: append([]byte(nil), refreshed...),
	}, nil
}
