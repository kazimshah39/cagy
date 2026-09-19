package accounts

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/kazimshah39/cagy/internal/securestate"
)

const (
	catalogFileName     = "accounts.json"
	transactionFileName = "account-transaction.json"
	maxCatalogBytes     = 1 << 20
	maxTransactionBytes = 64 << 10
)

type Repository struct {
	StateDir string
}

func NewRepository(stateDir string) Repository { return Repository{StateDir: stateDir} }

func (r Repository) LoadCatalog() (Catalog, error) {
	if _, err := securestate.InspectDir(r.StateDir); err != nil {
		return Catalog{}, err
	}
	data, exists, err := securestate.ReadFile(filepath.Join(r.StateDir, catalogFileName), maxCatalogBytes)
	if err != nil {
		return Catalog{}, fmt.Errorf("read account catalog: %w", err)
	}
	if !exists {
		return Catalog{Version: CatalogVersion, Accounts: []Account{}}, nil
	}
	var catalog Catalog
	if err := decodeStrict(data, &catalog); err != nil {
		return Catalog{}, fmt.Errorf("decode account catalog: %w", err)
	}
	if err := catalog.Validate(); err != nil {
		return Catalog{}, fmt.Errorf("validate account catalog: %w", err)
	}
	return catalog, nil
}

func (r Repository) SaveCatalog(catalog Catalog) error {
	catalog.Version = CatalogVersion
	catalog.Sort()
	if err := catalog.Validate(); err != nil {
		return err
	}
	data, err := encodeJSON(catalog, maxCatalogBytes)
	if err != nil {
		return fmt.Errorf("encode account catalog: %w", err)
	}
	if err := securestate.WriteFile(r.StateDir, catalogFileName, data); err != nil {
		return fmt.Errorf("write account catalog: %w", err)
	}
	return nil
}

func (r Repository) LoadTransaction() (Transaction, bool, error) {
	if _, err := securestate.InspectDir(r.StateDir); err != nil {
		return Transaction{}, false, err
	}
	data, exists, err := securestate.ReadFile(filepath.Join(r.StateDir, transactionFileName), maxTransactionBytes)
	if err != nil {
		return Transaction{}, exists, fmt.Errorf("read account transaction: %w", err)
	}
	if !exists {
		return Transaction{}, false, nil
	}
	var transaction Transaction
	if err := decodeStrict(data, &transaction); err != nil {
		return Transaction{}, true, fmt.Errorf("decode account transaction: %w", err)
	}
	if err := transaction.Validate(); err != nil {
		return Transaction{}, true, fmt.Errorf("validate account transaction: %w", err)
	}
	return transaction, true, nil
}

func (r Repository) SaveTransaction(transaction Transaction) error {
	transaction.Version = TransactionVersion
	if err := transaction.Validate(); err != nil {
		return err
	}
	data, err := encodeJSON(transaction, maxTransactionBytes)
	if err != nil {
		return fmt.Errorf("encode account transaction: %w", err)
	}
	if err := securestate.WriteFile(r.StateDir, transactionFileName, data); err != nil {
		return fmt.Errorf("write account transaction: %w", err)
	}
	return nil
}

func (r Repository) RemoveTransaction() error {
	if err := securestate.RemoveFile(r.StateDir, transactionFileName); err != nil {
		return fmt.Errorf("remove account transaction: %w", err)
	}
	return nil
}

func decodeStrict(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing JSON value")
		}
		return fmt.Errorf("decode trailing JSON: %w", err)
	}
	return nil
}

func encodeJSON(value any, maxBytes int) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if len(data) > maxBytes {
		return nil, fmt.Errorf("JSON exceeds %d bytes", maxBytes)
	}
	return data, nil
}
