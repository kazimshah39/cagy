//go:build !darwin || !arm64 || !cgo

package keychain

import (
	"context"
	"errors"
)

type unsupportedStore struct{}

func New() Store { return unsupportedStore{} }
func (unsupportedStore) Read(context.Context, Item) ([]byte, error) {
	return nil, errors.New("keychain requires Apple Silicon macOS with cgo")
}
func (unsupportedStore) Save(context.Context, Item, []byte, Access) error {
	return errors.New("keychain requires Apple Silicon macOS with cgo")
}
func (unsupportedStore) Delete(context.Context, Item) error {
	return errors.New("keychain requires Apple Silicon macOS with cgo")
}
func (unsupportedStore) Exists(context.Context, Item) (bool, error) {
	return false, errors.New("keychain requires Apple Silicon macOS with cgo")
}
