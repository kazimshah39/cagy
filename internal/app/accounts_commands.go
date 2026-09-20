package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/kazimshah39/cagy/internal/accounts"
	"github.com/kazimshah39/cagy/internal/keychain"
	"github.com/kazimshah39/cagy/internal/securestate"
)

func (a *App) accountService() (*accounts.AccountService, error) {
	if a.accountsFactory != nil {
		return a.accountsFactory()
	}
	store := keychain.New()
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve cagy executable: %w", err)
	}
	agyExecutable, err := a.runner.LookPath("agy")
	if err != nil {
		return nil, fmt.Errorf("resolve agy executable: %w", err)
	}
	access := keychain.Access{Label: "cagy agy account", TrustedPaths: []string{executable, agyExecutable}}
	repository := accounts.NewRepository(a.stateDir)
	acquire := func() (accounts.OperationLock, error) {
		lock, err := securestate.Acquire(a.stateDir, securestate.LockOptions{Name: "accounts.lock", Subject: "accounts", OperationID: "account-command", Now: a.now()})
		if err != nil {
			return nil, err
		}
		return lock, nil
	}
	identity := accounts.HTTPIdentityResolver{}
	canonical := keychain.CanonicalStore{Store: store, Access: access}
	service := &accounts.AccountService{
		Repository: repository,
		// Account snapshots use private local files in simplicity-first mode.
		// This avoids touching existing Keychain ACLs during normal startup.
		Vault:     accounts.FileCredentialVault{StateDir: a.stateDir},
		Canonical: canonical,
		Identity:  identity,
		Validator: storedAccountValidator{identity: identity, refresher: accounts.GoogleCredentialRefresher{}, now: a.now},
		Acquire:   acquire,
		Now:       a.now,
		Log:       a.debugf,
	}
	return service, nil
}

func readPassphraseFromTerminal(confirm bool) ([]byte, error) {
	input, ok := any(os.Stdin).(*os.File)
	if !ok {
		return nil, errors.New("passphrase requires an interactive terminal")
	}
	first, err := term.ReadPassword(int(input.Fd()))
	if err != nil {
		return nil, errors.New("read backup passphrase")
	}
	fmt.Fprintln(os.Stderr)
	if len(first) == 0 {
		return nil, errors.New("backup passphrase cannot be empty")
	}
	if confirm {
		second, secondErr := term.ReadPassword(int(input.Fd()))
		fmt.Fprintln(os.Stderr)
		if secondErr != nil || string(first) != string(second) {
			accounts.Zero(first)
			accounts.Zero(second)
			return nil, errors.New("backup passphrases do not match")
		}
		accounts.Zero(second)
	}
	return first, nil
}

func (a *App) accountsCommand(ctx context.Context, args []string) (runErr error) {
	command := "help"
	if len(args) > 0 {
		command = args[0]
	}
	a.debugf("accounts command begin name=%q argc=%d", command, len(args))
	defer func() {
		a.debugf("accounts command end name=%q ok=%t error=%q", command, runErr == nil, runErr)
	}()
	if err := a.checkPlatform(); err != nil {
		return err
	}
	service, err := a.accountService()
	if err != nil {
		return err
	}
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		a.printAccountsHelp()
		return nil
	}
	switch args[0] {
	case "list":
		return a.accountsList(service)
	case "status":
		if len(args) > 2 {
			return errors.New("usage: cagy accounts status [ACCOUNT]")
		}
		return a.accountsStatus(service, strings.TrimSpace(strings.Join(args[1:], " ")))
	case "import-active":
		if len(args) != 1 {
			return errors.New("usage: cagy accounts import-active")
		}
		account, err := service.ImportActive(ctx)
		if err != nil {
			return err
		}
		a.debugf("accounts import-active success account=%q", debugAccountID(account.ID))
		fmt.Fprintf(a.stdout, "Imported %s (%s)\n", account.Label, account.Email)
		return nil
	case "add":
		return a.accountsAdd(ctx, service, args[1:])
	case "switch":
		if len(args) == 2 && args[1] == "--auto" {
			return a.accountsSwitchAuto(ctx, service)
		}
		if len(args) != 2 || args[1] == "" || strings.HasPrefix(args[1], "--") {
			return errors.New("usage: cagy accounts switch ACCOUNT | cagy accounts switch --auto")
		}
		catalog, err := service.Repository.LoadCatalog()
		if err != nil {
			return err
		}
		target, err := resolveAccount(catalog, args[1])
		if err != nil {
			return err
		}
		account, err := service.Switch(ctx, target.ID)
		if err != nil {
			return err
		}
		a.debugf("accounts switch success account=%q", debugAccountID(account.ID))
		fmt.Fprintf(a.stdout, "Switched to %s (%s)\n", account.Label, account.Email)
		return nil
	case "remove":
		if len(args) != 3 || args[1] == "" || args[2] != "--yes" {
			return errors.New("usage: cagy accounts remove ACCOUNT --yes")
		}
		catalog, err := service.Repository.LoadCatalog()
		if err != nil {
			return err
		}
		target, err := resolveAccount(catalog, args[1])
		if err != nil {
			return err
		}
		if err := service.Remove(ctx, target.ID, true, a.boundAccountIDs()); err != nil {
			return err
		}
		a.debugf("accounts remove success account=%q", debugAccountID(target.ID))
		fmt.Fprintln(a.stdout, "Account removed from cagy")
		return nil
	case "doctor":
		if len(args) != 1 {
			return errors.New("usage: cagy accounts doctor")
		}
		report, err := service.Doctor(ctx)
		if err != nil {
			return err
		}
		if len(report.Issues) == 0 {
			fmt.Fprintln(a.stdout, "✓ cagy account state is healthy")
			return nil
		}
		for _, issue := range report.Issues {
			fmt.Fprintf(a.stdout, "✗ %s\n", issue)
		}
		return errors.New("account doctor found problems")
	case "export":
		return a.accountsExport(ctx, service, args[1:])
	case "import":
		return a.accountsImport(ctx, service, args[1:])
	default:
		return fmt.Errorf("unknown cagy accounts command %q", args[0])
	}
}

func (a *App) accountsSwitchAuto(ctx context.Context, service *accounts.AccountService) error {
	catalog, err := service.Repository.LoadCatalog()
	if err != nil {
		return err
	}
	currentID := strings.TrimSpace(catalog.DefaultAccountID)
	if currentID == "" {
		return errors.New("no default cagy account is configured; run cagy accounts import-active or cagy accounts add")
	}
	current, found := catalog.Find(currentID)
	if !found {
		return errors.New("default cagy account is not present in the account catalog")
	}
	probe := a.probeAgyQuota(ctx)
	switch probe.Class {
	case quotaAvailable:
		fmt.Fprintf(a.stdout, "Current account %s (%s) is healthy; no switch needed\n", current.Label, current.Email)
		return nil
	case quotaUnknown:
		return fmt.Errorf("cannot auto-switch because current account quota is unknown: %s", probe.Reason)
	}
	result, err := a.rotateAccounts(ctx, service, current.ID, probe, map[string]struct{}{}, rotationHooks{
		Probe: func(ctx context.Context) quotaProbeResult { return a.probeAgyQuota(ctx) },
	})
	if err != nil {
		// Restore the original canonical credential without moving the cursor;
		// then reapply its confirmed quota evidence instead of replacing it with
		// the validator's temporary unknown result.
		if _, restoreErr := service.Switch(ctx, current.ID); restoreErr == nil {
			_ = service.RecordQuota(current.ID, probe.snapshot())
		}
		return err
	}
	weekly, five := debugQuotaRemaining(result.Quota.WeeklyRemaining), debugQuotaRemaining(result.Quota.FiveHourRemaining)
	fmt.Fprintf(a.stdout, "Switched to %s (%s); weekly remaining %s, five-hour remaining %s. Restart agy to use this account.\n", result.Account.Label, result.Account.Email, weekly, five)
	return nil
}

func (a *App) accountsList(service *accounts.AccountService) error {
	catalog, err := service.Repository.LoadCatalog()
	if err != nil {
		return err
	}
	if len(catalog.Accounts) == 0 {
		a.debugf("accounts list count=0")
		fmt.Fprintln(a.stdout, "No cagy accounts. Run: cagy accounts import-active or cagy accounts add")
		return nil
	}
	a.debugf("accounts list count=%d default=%q", len(catalog.Accounts), debugAccountID(catalog.DefaultAccountID))
	for _, account := range catalog.Accounts {
		marker := " "
		if account.ID == catalog.DefaultAccountID {
			marker = "*"
		}
		fmt.Fprintf(a.stdout, "%s %s  %s  %s  %s\n", marker, account.ID[:8], account.Label, account.Email, account.State)
	}
	return nil
}

func (a *App) accountsStatus(service *accounts.AccountService, reference string) error {
	catalog, err := service.Repository.LoadCatalog()
	if err != nil {
		return err
	}
	if reference == "" {
		reference = catalog.DefaultAccountID
	}
	account, err := resolveAccount(catalog, reference)
	if err != nil {
		return err
	}
	a.debugf("accounts status account=%q state=%q default=%t quota_present=%t", debugAccountID(account.ID), account.State, account.ID == catalog.DefaultAccountID, account.Quota != nil)
	fmt.Fprintf(a.stdout, "ID: %s\nLabel: %s\nEmail: %s\nState: %s\n", account.ID, account.Label, account.Email, account.State)
	if account.ID == catalog.DefaultAccountID {
		fmt.Fprintln(a.stdout, "Default: yes")
	}
	if account.Quota != nil {
		fmt.Fprintf(a.stdout, "Quota: %s (observed %s)\n", account.Quota.Class, account.Quota.ObservedAt.Format("2006-01-02 15:04:05 MST"))
	}
	return nil
}

func resolveAccount(catalog accounts.Catalog, reference string) (accounts.Account, error) {
	for _, account := range catalog.Accounts {
		if account.ID == reference || account.Email == reference {
			return account, nil
		}
	}
	var matches []accounts.Account
	for _, account := range catalog.Accounts {
		if strings.EqualFold(account.Label, reference) {
			matches = append(matches, account)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return accounts.Account{}, errors.New("account reference is ambiguous")
	}
	return accounts.Account{}, errors.New("account was not found")
}

func (a *App) accountsAdd(ctx context.Context, service *accounts.AccountService, args []string) error {
	if len(args) != 0 {
		return errors.New("usage: cagy accounts add")
	}
	if a.accountLogin == nil || a.openURL == nil {
		return errors.New("Google account login is unavailable")
	}
	a.debugf("accounts add begin mode=%q", "local-oauth")
	credential, err := a.accountLogin(ctx, func(target string) error {
		// Opening a browser is allowed only for this explicit add command. Never
		// log the authorization URL because it contains short-lived flow state.
		return a.openURL(ctx, target)
	})
	if err != nil {
		a.debugf("accounts add login-error error=%q", err)
		return err
	}
	defer accounts.Zero(credential)
	account, err := service.EnrollCredential(ctx, credential)
	if err != nil {
		a.debugf("accounts add enroll-error error=%q", err)
		return err
	}
	a.debugf("accounts add success account=%q", debugAccountID(account.ID))
	fmt.Fprintf(a.stdout, "Added %s (%s) to cagy; agy's current login was not changed\n", account.Label, account.Email)
	return nil
}

func (a *App) accountsExport(ctx context.Context, service *accounts.AccountService, args []string) error {
	_ = ctx
	metadataOnly := false
	force := false
	var output string
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--metadata-only":
			metadataOnly = true
		case "--force":
			force = true
		case "--output":
			if index+1 >= len(args) {
				return errors.New("--output requires an absolute file path")
			}
			output = args[index+1]
			index++
		default:
			return errors.New("usage: cagy accounts export [--metadata-only] --output FILE [--force]")
		}
	}
	if output == "" || !filepath.IsAbs(output) {
		return errors.New("--output requires an absolute file path")
	}
	a.debugf("accounts export begin metadata_only=%t force=%t output=%q", metadataOnly, force, output)
	catalog, err := service.Repository.LoadCatalog()
	if err != nil {
		return err
	}
	if metadataOnly {
		if err := accounts.ExportMetadata(output, catalog, force, a.now()); err != nil {
			return err
		}
		a.debugf("accounts export success metadata_only=true accounts=%d output=%q", len(catalog.Accounts), output)
		fmt.Fprintf(a.stdout, "Metadata exported to %s\n", output)
		return nil
	}
	passphrase, err := a.readPassphrase(true)
	if err != nil {
		return err
	}
	defer accounts.Zero(passphrase)
	credentials := make(map[string][]byte, len(catalog.Accounts))
	defer func() {
		for _, credential := range credentials {
			accounts.Zero(credential)
		}
	}()
	for _, account := range catalog.Accounts {
		credential, loadErr := service.Vault.Load(ctx, account.ID)
		if loadErr != nil {
			return fmt.Errorf("load credential for %s: %w", account.ID[:8], loadErr)
		}
		credentials[account.ID] = credential
	}
	if err := accounts.ExportEncrypted(output, catalog, credentials, passphrase, force, a.now()); err != nil {
		return err
	}
	a.debugf("accounts export success metadata_only=false accounts=%d output=%q", len(catalog.Accounts), output)
	fmt.Fprintf(a.stdout, "Encrypted account backup exported to %s\n", output)
	return nil
}

func (a *App) accountsImport(ctx context.Context, service *accounts.AccountService, args []string) error {
	if len(args) != 1 || !filepath.IsAbs(args[0]) {
		return errors.New("usage: cagy accounts import ABSOLUTE_FILE")
	}
	a.debugf("accounts import begin input=%q", args[0])
	passphrase, err := a.readPassphrase(false)
	if err != nil {
		return err
	}
	defer accounts.Zero(passphrase)
	catalog, credentials, err := accounts.ImportEncrypted(args[0], passphrase)
	if err != nil {
		return err
	}
	defer func() {
		for _, credential := range credentials {
			accounts.Zero(credential)
		}
	}()
	if err := service.ImportBackup(ctx, catalog, credentials); err != nil {
		return err
	}
	a.debugf("accounts import success input=%q accounts=%d", args[0], len(catalog.Accounts))
	fmt.Fprintf(a.stdout, "Imported %d account(s) without activating them\n", len(catalog.Accounts))
	return nil
}

func (a *App) boundAccountIDs() map[string]struct{} {
	bound := map[string]struct{}{}
	paths, err := filepath.Glob(filepath.Join(a.stateDir, "account-binding-*.json"))
	if err != nil {
		return bound
	}
	for _, path := range paths {
		data, exists, readErr := securestate.ReadFile(path, 16<<10)
		if readErr != nil || !exists {
			continue
		}
		var binding accountBinding
		if json.Unmarshal(data, &binding) == nil && validOpaqueAccountID(binding.AccountID) {
			bound[binding.AccountID] = struct{}{}
		}
	}
	a.debugf("accounts bindings scanned files=%d valid_accounts=%d", len(paths), len(bound))
	return bound
}

func (a *App) printAccountsHelp() {
	io.WriteString(a.stdout, `cagy accounts - manage agy CLI accounts

Usage:
  cagy accounts add
  cagy accounts import-active
  cagy accounts list
  cagy accounts status [ACCOUNT]
  cagy accounts switch ACCOUNT
  cagy accounts remove ACCOUNT --yes
  cagy accounts doctor
  cagy accounts export --output FILE [--force]
  cagy accounts export --metadata-only --output FILE [--force]
  cagy accounts import FILE
`)
}

var _ = term.IsTerminal
