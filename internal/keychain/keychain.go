package keychain

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	CanonicalService = "gemini"
	CanonicalAccount = "antigravity"
	VaultService     = "com.kazimshah39.cagy.agy-account"
)

type ErrorKind string

const (
	ErrorNotFound          ErrorKind = "not_found"
	ErrorDuplicate         ErrorKind = "duplicate"
	ErrorDenied            ErrorKind = "denied"
	ErrorInteractionNeeded ErrorKind = "interaction_required"
	ErrorInvalidItem       ErrorKind = "invalid_item"
	ErrorSystem            ErrorKind = "system"
)

// Error intentionally contains no credential material.
type Error struct {
	Op     string
	Kind   ErrorKind
	Status int32
}

func (e *Error) Error() string {
	if e == nil {
		return "keychain error"
	}
	return fmt.Sprintf("keychain %s failed (%s, status %d)", e.Op, e.Kind, e.Status)
}

func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	return ok && (other.Kind == "" || e.Kind == other.Kind)
}

var (
	ErrNotFound          = &Error{Kind: ErrorNotFound}
	ErrDuplicate         = &Error{Kind: ErrorDuplicate}
	ErrDenied            = &Error{Kind: ErrorDenied}
	ErrInteractionNeeded = &Error{Kind: ErrorInteractionNeeded}
	ErrInvalidItem       = &Error{Kind: ErrorInvalidItem}
)

// Item identifies one generic-password item. It never contains the secret.
type Item struct {
	Service string
	Account string
}

func (i Item) validate() error {
	if i.Service == "" || i.Account == "" {
		return fmt.Errorf("keychain item service and account are required")
	}
	return nil
}

// Access carries the display metadata used when creating a Keychain item.
// On the user's personal Mac, cagy intentionally uses the AGM-compatible
// allow-local-applications policy so normal operations do not block on prompts.
// TrustedPaths remains part of the boundary for API compatibility and future
// policy tightening, but the current user-selected mode does not restrict apps.
type Access struct {
	Label        string
	TrustedPaths []string
}

// Store is the narrow secret-store boundary used by account services.
type Store interface {
	Read(context.Context, Item) ([]byte, error)
	Save(context.Context, Item, []byte, Access) error
	Delete(context.Context, Item) error
	Exists(context.Context, Item) (bool, error)
}

// CredentialVault stores credentials under cagy's private Keychain service.
type CredentialVault struct {
	Store  Store
	Access Access
}

func (v CredentialVault) Load(ctx context.Context, accountID string) ([]byte, error) {
	return v.Store.Read(ctx, Item{Service: VaultService, Account: accountID})
}

func (v CredentialVault) Save(ctx context.Context, accountID string, secret []byte) error {
	return v.Store.Save(ctx, Item{Service: VaultService, Account: accountID}, secret, v.Access)
}

func (v CredentialVault) Delete(ctx context.Context, accountID string) error {
	err := v.Store.Delete(ctx, Item{Service: VaultService, Account: accountID})
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

func (v CredentialVault) Exists(ctx context.Context, accountID string) (bool, error) {
	return v.Store.Exists(ctx, Item{Service: VaultService, Account: accountID})
}

// CanonicalStore manages only agy's canonical Keychain item and a transaction-scoped quarantine item.
type CanonicalStore struct {
	Store  Store
	Access Access
}

func (s CanonicalStore) Read(ctx context.Context) ([]byte, error) {
	return s.Store.Read(ctx, Item{Service: CanonicalService, Account: CanonicalAccount})
}

type canonicalReplacer interface {
	ReplaceCanonical(context.Context, []byte, Access) error
}

func (s CanonicalStore) Replace(ctx context.Context, secret []byte) error {
	if replacer, ok := s.Store.(canonicalReplacer); ok {
		return replacer.ReplaceCanonical(ctx, secret, s.Access)
	}
	return s.Store.Save(ctx, Item{Service: CanonicalService, Account: CanonicalAccount}, secret, s.Access)
}

func canonicalKeychainPayload(secret []byte) ([]byte, error) {
	payload := bytes.TrimSpace(secret)
	if len(payload) == 0 || len(payload) > 1<<20 {
		return nil, errors.New("agy credential payload has an invalid size")
	}
	const prefix = "go-keyring-base64:"
	if bytes.HasPrefix(payload, []byte(prefix)) {
		decoded, err := base64.StdEncoding.DecodeString(string(payload[len(prefix):]))
		if err != nil || len(decoded) == 0 || !json.Valid(decoded) {
			Zero(decoded)
			return nil, errors.New("agy credential payload encoding is invalid")
		}
		Zero(decoded)
		return append([]byte(nil), payload...), nil
	}
	if payload[0] != '{' {
		decoded, err := base64.StdEncoding.DecodeString(string(payload))
		if err != nil || len(decoded) == 0 || !json.Valid(decoded) {
			Zero(decoded)
			return nil, errors.New("agy credential payload format is unsupported")
		}
		payload = decoded
		defer Zero(decoded)
	}
	if !json.Valid(payload) {
		return nil, errors.New("agy credential payload is invalid")
	}
	return []byte(prefix + base64.StdEncoding.EncodeToString(payload)), nil
}

// Zero clears one temporary credential buffer on a best-effort basis.
func Zero(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
