package app

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/kazimshah39/cagy/internal/herdr"
	"github.com/kazimshah39/cagy/internal/securestate"
)

const accountBindingVersion = 1

type accountBinding struct {
	Version   int    `json:"version"`
	Developer string `json:"developer"`
	AccountID string `json:"account_id"`
}

func (a *App) accountBindingPath(developer string) string {
	return filepath.Join(a.stateDir, securestate.HashedName("account-binding", developer, ".json"))
}

func (a *App) loadAccountBinding(developer string) (accountBinding, bool, error) {
	a.debugf("account-binding load begin developer=%q", developer)
	data, exists, err := securestate.ReadFile(a.accountBindingPath(developer), 16<<10)
	if err != nil || !exists {
		a.debugf("account-binding load result developer=%q exists=%t ok=%t error=%q", developer, exists, err == nil, err)
		return accountBinding{}, exists, err
	}
	var binding accountBinding
	if err := json.Unmarshal(data, &binding); err != nil {
		a.debugf("account-binding load corrupt developer=%q bytes=%d", developer, len(data))
		return accountBinding{}, true, errors.New("account binding is corrupt")
	}
	if binding.Version != accountBindingVersion || binding.Developer != developer || binding.AccountID == "" {
		a.debugf("account-binding load invalid developer=%q version=%d account_present=%t", developer, binding.Version, binding.AccountID != "")
		return accountBinding{}, true, errors.New("account binding is invalid")
	}
	a.debugf("account-binding load success developer=%q account=%q", developer, debugAccountID(binding.AccountID))
	return binding, true, nil
}

func (a *App) saveAccountBinding(developer, accountID string) error {
	a.debugf("account-binding save begin developer=%q account=%q", developer, debugAccountID(accountID))
	if developer == "" || accountID == "" {
		return errors.New("account binding requires developer and account ID")
	}
	if !validOpaqueAccountID(accountID) {
		return errors.New("account binding account ID is invalid")
	}
	data, err := json.Marshal(accountBinding{Version: accountBindingVersion, Developer: developer, AccountID: accountID})
	if err != nil {
		return err
	}
	err = securestate.WriteFile(a.stateDir, filepath.Base(a.accountBindingPath(developer)), append(data, '\n'))
	a.debugf("account-binding save end developer=%q account=%q ok=%t error=%q", developer, debugAccountID(accountID), err == nil, err)
	return err
}

func (a *App) bindDeveloperAccount(ctx context.Context, developer, paneID, accountID string) error {
	a.debugf("account-binding persist begin developer=%q pane=%q account=%q", developer, paneID, debugAccountID(accountID))
	if !validOpaqueAccountID(accountID) {
		return errors.New("agy account binding ID is invalid")
	}
	if err := a.herdr.ReportPaneAccount(ctx, paneID, paneOwnershipSource, accountID); err != nil {
		return fmt.Errorf("persist agy account binding in Herdr: %w", err)
	}
	if err := a.saveAccountBinding(developer, accountID); err != nil {
		return fmt.Errorf("persist agy account binding: %w", err)
	}
	a.debugf("account-binding persist success developer=%q pane=%q account=%q", developer, paneID, debugAccountID(accountID))
	return nil
}

func validOpaqueAccountID(accountID string) bool {
	if len(accountID) != 64 {
		return false
	}
	_, err := hex.DecodeString(accountID)
	return err == nil
}

func (a *App) boundAccountID(developer string, pane herdr.PaneInfo) (string, error) {
	if token := strings.TrimSpace(pane.Tokens["cagy_account_id"]); token != "" {
		if !validOpaqueAccountID(token) {
			return "", errors.New("developer pane has an invalid cagy account binding")
		}
		a.debugf("account-binding resolve developer=%q source=%q account=%q", developer, "herdr-pane", debugAccountID(token))
		return token, nil
	}
	binding, exists, err := a.loadAccountBinding(developer)
	if err != nil {
		return "", err
	}
	if exists {
		a.debugf("account-binding resolve developer=%q source=%q account=%q", developer, "local-state", debugAccountID(binding.AccountID))
		return binding.AccountID, nil
	}
	a.debugf("account-binding resolve developer=%q source=%q", developer, "none")
	return "", nil
}
