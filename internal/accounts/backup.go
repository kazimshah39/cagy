package accounts

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"golang.org/x/crypto/argon2"
)

const (
	backupMagic           = "CAGYAGY1"
	backupVersion         = 1
	backupKDF             = "argon2id"
	backupCipher          = "aes-256-gcm"
	backupMemoryKiB       = 64 * 1024
	backupIterations      = 3
	maxBackupMemoryKiB    = 512 * 1024
	maxBackupIterations   = 10
	maxBackupParallelism  = 8
	maxBackupBytes        = 32 << 20
	maxBackupAccounts     = 1000
	metadataExportVersion = 3
)

type backupHeader struct {
	Version       int       `json:"version"`
	KDF           string    `json:"kdf"`
	Cipher        string    `json:"cipher"`
	MemoryKiB     uint32    `json:"memory_kib"`
	Iterations    uint32    `json:"iterations"`
	Parallelism   uint8     `json:"parallelism"`
	Salt          string    `json:"salt"`
	Nonce         string    `json:"nonce"`
	CreatedAt     time.Time `json:"created_at"`
	PayloadLength uint64    `json:"payload_length"`
}

type backupPayload struct {
	Version     int               `json:"version"`
	Catalog     Catalog           `json:"catalog"`
	Credentials map[string][]byte `json:"credentials"`
}

type MetadataExport struct {
	Version          int               `json:"version"`
	CreatedAt        time.Time         `json:"created_at"`
	DefaultAccountID string            `json:"default_account_id,omitempty"`
	Rotation         *RotationState    `json:"rotation,omitempty"`
	Accounts         []MetadataAccount `json:"accounts"`
}

type MetadataAccount struct {
	ID             string         `json:"id"`
	Provider       string         `json:"provider"`
	Label          string         `json:"label"`
	Email          string         `json:"email"`
	State          State          `json:"state"`
	LastVerifiedAt time.Time      `json:"last_verified_at,omitempty"`
	LastUsedAt     time.Time      `json:"last_used_at,omitempty"`
	CooldownUntil  time.Time      `json:"cooldown_until,omitempty"`
	Quota          *QuotaSnapshot `json:"quota_snapshot,omitempty"`
}

func ExportEncrypted(path string, catalog Catalog, credentials map[string][]byte, passphrase []byte, force bool, now time.Time) error {
	if len(passphrase) == 0 {
		return errors.New("backup passphrase cannot be empty")
	}
	if err := validateBackupRecords(catalog, credentials); err != nil {
		return err
	}
	if now.IsZero() {
		now = time.Now()
	}
	payloadBytes, err := json.Marshal(backupPayload{Version: backupVersion, Catalog: catalog, Credentials: credentials})
	if err != nil {
		return fmt.Errorf("encode backup payload: %w", err)
	}
	defer clearBytes(payloadBytes)
	if len(payloadBytes) > maxBackupBytes/2 {
		return errors.New("backup payload is too large")
	}
	salt := make([]byte, 16)
	nonce := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return fmt.Errorf("create backup salt: %w", err)
	}
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("create backup nonce: %w", err)
	}
	parallelism := uint8(runtime.NumCPU())
	if parallelism < 1 {
		parallelism = 1
	}
	if parallelism > 4 {
		parallelism = 4
	}
	key := argon2.IDKey(passphrase, salt, backupIterations, backupMemoryKiB, parallelism, 32)
	defer clearBytes(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return errors.New("create backup cipher")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return errors.New("create backup encryption mode")
	}
	header := backupHeader{
		Version: backupVersion, KDF: backupKDF, Cipher: backupCipher,
		MemoryKiB: backupMemoryKiB, Iterations: backupIterations, Parallelism: parallelism,
		Salt: base64.StdEncoding.EncodeToString(salt), Nonce: base64.StdEncoding.EncodeToString(nonce),
		CreatedAt: now.UTC(), PayloadLength: uint64(len(payloadBytes) + gcm.Overhead()),
	}
	headerBytes, err := json.Marshal(header)
	if err != nil {
		return fmt.Errorf("encode backup header: %w", err)
	}
	if len(headerBytes) > 64<<10 {
		return errors.New("backup header is too large")
	}
	prefix := make([]byte, len(backupMagic)+4+len(headerBytes))
	copy(prefix, backupMagic)
	binary.BigEndian.PutUint32(prefix[len(backupMagic):], uint32(len(headerBytes)))
	copy(prefix[len(backupMagic)+4:], headerBytes)
	ciphertext := gcm.Seal(nil, nonce, payloadBytes, prefix)
	archive := append(prefix, ciphertext...)
	defer clearBytes(ciphertext)
	if len(archive) > maxBackupBytes {
		return errors.New("backup archive is too large")
	}
	return writeOutputFile(path, archive, force)
}

func ImportEncrypted(path string, passphrase []byte) (Catalog, map[string][]byte, error) {
	if len(passphrase) == 0 {
		return Catalog{}, nil, errors.New("backup passphrase cannot be empty")
	}
	archive, err := readOutputFile(path, maxBackupBytes)
	if err != nil {
		return Catalog{}, nil, err
	}
	defer clearBytes(archive)
	if len(archive) < len(backupMagic)+4 || string(archive[:len(backupMagic)]) != backupMagic {
		return Catalog{}, nil, errors.New("backup magic is invalid")
	}
	headerLength := int(binary.BigEndian.Uint32(archive[len(backupMagic) : len(backupMagic)+4]))
	prefixLength := len(backupMagic) + 4 + headerLength
	if headerLength <= 0 || headerLength > 64<<10 || prefixLength > len(archive) {
		return Catalog{}, nil, errors.New("backup header length is invalid")
	}
	var header backupHeader
	if err := decodeStrict(archive[len(backupMagic)+4:prefixLength], &header); err != nil {
		return Catalog{}, nil, errors.New("backup header is invalid")
	}
	if err := validateBackupHeader(header, len(archive)-prefixLength); err != nil {
		return Catalog{}, nil, err
	}
	salt, err := base64.StdEncoding.DecodeString(header.Salt)
	if err != nil || len(salt) != 16 {
		return Catalog{}, nil, errors.New("backup salt is invalid")
	}
	nonce, err := base64.StdEncoding.DecodeString(header.Nonce)
	if err != nil || len(nonce) != 12 {
		return Catalog{}, nil, errors.New("backup nonce is invalid")
	}
	key := argon2.IDKey(passphrase, salt, header.Iterations, header.MemoryKiB, header.Parallelism, 32)
	defer clearBytes(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return Catalog{}, nil, errors.New("create backup cipher")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return Catalog{}, nil, errors.New("create backup encryption mode")
	}
	plaintext, err := gcm.Open(nil, nonce, archive[prefixLength:], archive[:prefixLength])
	if err != nil {
		return Catalog{}, nil, errors.New("backup authentication failed; passphrase is wrong or the file was changed")
	}
	defer clearBytes(plaintext)
	var payload backupPayload
	if err := decodeStrict(plaintext, &payload); err != nil {
		return Catalog{}, nil, errors.New("backup payload is invalid")
	}
	if payload.Version != backupVersion {
		return Catalog{}, nil, fmt.Errorf("unsupported backup payload version %d", payload.Version)
	}
	if err := validateBackupRecords(payload.Catalog, payload.Credentials); err != nil {
		return Catalog{}, nil, fmt.Errorf("validate backup payload: %w", err)
	}
	credentials := make(map[string][]byte, len(payload.Credentials))
	for id, credential := range payload.Credentials {
		credentials[id] = append([]byte(nil), credential...)
	}
	return payload.Catalog, credentials, nil
}

func ExportMetadata(path string, catalog Catalog, force bool, now time.Time) error {
	if err := catalog.Validate(); err != nil {
		return err
	}
	if now.IsZero() {
		now = time.Now()
	}
	rotation := catalog.Rotation
	if rotation != nil {
		copyRotation := *rotation
		copyRotation.Order = append([]string(nil), rotation.Order...)
		rotation = &copyRotation
	}
	export := MetadataExport{Version: metadataExportVersion, CreatedAt: now.UTC(), DefaultAccountID: catalog.DefaultAccountID, Rotation: rotation}
	for _, account := range catalog.Accounts {
		export.Accounts = append(export.Accounts, MetadataAccount{
			ID: account.ID, Provider: account.Provider, Label: account.Label, Email: account.Email,
			State: account.State, LastVerifiedAt: account.LastVerifiedAt, LastUsedAt: account.LastUsedAt,
			CooldownUntil: account.CooldownUntil, Quota: account.Quota,
		})
	}
	data, err := json.MarshalIndent(export, "", "  ")
	if err != nil {
		return fmt.Errorf("encode metadata export: %w", err)
	}
	data = append(data, '\n')
	return writeOutputFile(path, data, force)
}

func validateBackupHeader(header backupHeader, ciphertextLength int) error {
	if header.Version != backupVersion {
		return fmt.Errorf("unsupported backup version %d", header.Version)
	}
	if header.KDF != backupKDF || header.Cipher != backupCipher {
		return errors.New("backup algorithms are unsupported")
	}
	if header.MemoryKiB < backupMemoryKiB || header.MemoryKiB > maxBackupMemoryKiB {
		return errors.New("backup memory parameter is unsafe")
	}
	if header.Iterations < backupIterations || header.Iterations > maxBackupIterations {
		return errors.New("backup iteration parameter is unsafe")
	}
	if header.Parallelism < 1 || header.Parallelism > maxBackupParallelism {
		return errors.New("backup parallelism parameter is unsafe")
	}
	if header.CreatedAt.IsZero() || header.PayloadLength != uint64(ciphertextLength) {
		return errors.New("backup header metadata is inconsistent")
	}
	return nil
}

func validateBackupRecords(catalog Catalog, credentials map[string][]byte) error {
	if err := catalog.Validate(); err != nil {
		return fmt.Errorf("invalid backup catalog: %w", err)
	}
	if len(catalog.Accounts) > maxBackupAccounts || len(credentials) > maxBackupAccounts {
		return errors.New("backup has too many accounts")
	}
	if len(credentials) != len(catalog.Accounts) {
		return errors.New("backup credentials do not match the account catalog")
	}
	for _, account := range catalog.Accounts {
		credential, found := credentials[account.ID]
		if !found || len(credential) == 0 || len(credential) > maxCredentialBytes {
			return fmt.Errorf("backup credential is missing or invalid for account %s", account.ID)
		}
		fingerprint, err := CredentialFingerprint(credential)
		if err != nil || (account.CredentialFingerprint != "" && fingerprint != account.CredentialFingerprint) {
			return fmt.Errorf("backup credential fingerprint does not match account %s", account.ID)
		}
	}
	for id := range credentials {
		if _, found := catalog.Find(id); !found {
			return fmt.Errorf("backup contains an unknown credential account %s", id)
		}
	}
	return nil
}

func readOutputFile(path string, limit int64) ([]byte, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("backup path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect backup: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("backup is not a regular file")
	}
	if info.Size() > limit {
		return nil, errors.New("backup is too large")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open backup: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("backup changed while it was opened")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("read backup failed or exceeded the size limit")
	}
	return data, nil
}

func writeOutputFile(path string, data []byte, force bool) error {
	if path == "" || !filepath.IsAbs(path) {
		return errors.New("output path must be absolute")
	}
	parent := filepath.Dir(path)
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("output parent must be an existing regular directory")
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("output target is not a regular file")
		}
		if !force {
			return errors.New("output file already exists; use --force to replace it")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect output target: %w", err)
	}
	temporary, err := os.CreateTemp(parent, ".cagy-export-*.tmp")
	if err != nil {
		return fmt.Errorf("create output file: %w", err)
	}
	temporaryPath := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}
	if err := temporary.Chmod(0o600); err != nil {
		cleanup()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	directory, err := os.Open(parent)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
