package accounts

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	CatalogVersion     = 1
	TransactionVersion = 1
	ProviderGoogle     = "google"
)

type State string

const (
	StateHealthy    State = "healthy"
	StateLowQuota   State = "low_quota"
	StateUnknown    State = "unknown"
	StateNeedsLogin State = "needs_login"
	StateDisabled   State = "disabled"
)

type QuotaClass string

const (
	QuotaAvailable QuotaClass = "available"
	QuotaLow       QuotaClass = "low"
	QuotaExhausted QuotaClass = "exhausted"
	QuotaUnknown   QuotaClass = "unknown"
)

type QuotaSnapshot struct {
	Class             QuotaClass `json:"class"`
	Reason            string     `json:"reason,omitempty"`
	ModelID           string     `json:"model_id,omitempty"`
	Group             string     `json:"group,omitempty"`
	WeeklyRemaining   *float64   `json:"weekly_remaining,omitempty"`
	FiveHourRemaining *float64   `json:"five_hour_remaining,omitempty"`
	ObservedAt        time.Time  `json:"observed_at"`
}

type Account struct {
	ID                    string         `json:"id"`
	Provider              string         `json:"provider"`
	Label                 string         `json:"label"`
	Email                 string         `json:"email"`
	CredentialFingerprint string         `json:"credential_fingerprint,omitempty"`
	State                 State          `json:"state"`
	LastVerifiedAt        time.Time      `json:"last_verified_at,omitempty"`
	LastUsedAt            time.Time      `json:"last_used_at,omitempty"`
	LastFailureAt         time.Time      `json:"last_failure_at,omitempty"`
	ConsecutiveFailures   int            `json:"consecutive_failures,omitempty"`
	CooldownUntil         time.Time      `json:"cooldown_until,omitempty"`
	Quota                 *QuotaSnapshot `json:"quota_snapshot,omitempty"`
}

type Catalog struct {
	Version          int       `json:"version"`
	Revision         uint64    `json:"revision"`
	DefaultAccountID string    `json:"default_account_id,omitempty"`
	Accounts         []Account `json:"accounts"`
}

type TransactionKind string

type TransactionPhase string

const (
	TransactionAdd            TransactionKind = "add"
	TransactionSwitch         TransactionKind = "switch"
	TransactionProbe          TransactionKind = "probe"
	TransactionDeveloperStart TransactionKind = "developer_start"
	TransactionImport         TransactionKind = "import"

	PhasePrepared          TransactionPhase = "prepared"
	PhaseCanonicalIsolated TransactionPhase = "canonical_isolated"
	PhaseLoginRunning      TransactionPhase = "login_running"
	PhaseCandidateActive   TransactionPhase = "candidate_active"
	PhaseValidating        TransactionPhase = "validating"
	PhaseRestoring         TransactionPhase = "restoring"
	PhaseCommitted         TransactionPhase = "committed"
)

type Transaction struct {
	Version             int              `json:"version"`
	OperationID         string           `json:"operation_id"`
	Kind                TransactionKind  `json:"kind"`
	Phase               TransactionPhase `json:"phase"`
	StartedAt           time.Time        `json:"started_at"`
	UpdatedAt           time.Time        `json:"updated_at"`
	PreviousDefaultID   string           `json:"previous_default_id,omitempty"`
	PreviousCanonicalID string           `json:"previous_canonical_id,omitempty"`
	CandidateID         string           `json:"candidate_id,omitempty"`
	TargetDeveloper     string           `json:"target_developer,omitempty"`
	ActivateAfterAdd    bool             `json:"activate_after_add,omitempty"`
}

var (
	accountIDPattern   = regexp.MustCompile(`^[a-f0-9]{64}$`)
	fingerprintPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
	operationIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{7,127}$`)
	emailPattern       = regexp.MustCompile(`^[^\s@]+@[^\s@]+$`)
)

func StableID(providerSubject string) (string, error) {
	providerSubject = strings.TrimSpace(providerSubject)
	if providerSubject == "" {
		return "", errors.New("verified provider subject is required")
	}
	sum := sha256.Sum256([]byte(providerSubject))
	return hex.EncodeToString(sum[:]), nil
}

func CredentialFingerprint(secret []byte) (string, error) {
	if len(secret) == 0 {
		return "", errors.New("credential is empty")
	}
	sum := sha256.Sum256(secret)
	return hex.EncodeToString(sum[:]), nil
}

func NormalizeLabel(label string) (string, error) {
	label = strings.Join(strings.Fields(label), " ")
	if label == "" {
		return "", errors.New("account label is required")
	}
	if len([]rune(label)) > 80 {
		return "", errors.New("account label exceeds 80 characters")
	}
	for _, value := range label {
		if value < 0x20 || value == 0x7f {
			return "", errors.New("account label contains control characters")
		}
	}
	return label, nil
}

func (q QuotaSnapshot) Validate() error {
	switch q.Class {
	case QuotaAvailable, QuotaLow, QuotaExhausted, QuotaUnknown:
	default:
		return fmt.Errorf("invalid quota class %q", q.Class)
	}
	if q.ObservedAt.IsZero() {
		return errors.New("quota observation time is required")
	}
	for name, value := range map[string]*float64{"weekly": q.WeeklyRemaining, "five-hour": q.FiveHourRemaining} {
		if value != nil && (*value < 0 || *value > 1) {
			return fmt.Errorf("%s quota fraction is outside 0..1", name)
		}
	}
	if len(q.Reason) > 120 || len(q.ModelID) > 200 || len(q.Group) > 200 {
		return errors.New("quota metadata is too long")
	}
	return nil
}

func (a Account) Validate() error {
	if !accountIDPattern.MatchString(a.ID) {
		return errors.New("account has an invalid stable ID")
	}
	if a.Provider != ProviderGoogle {
		return fmt.Errorf("unsupported account provider %q", a.Provider)
	}
	label, err := NormalizeLabel(a.Label)
	if err != nil || label != a.Label {
		return errors.New("account has an invalid normalized label")
	}
	if len(a.Email) > 320 || !emailPattern.MatchString(a.Email) {
		return errors.New("account has an invalid email")
	}
	if a.CredentialFingerprint != "" && !fingerprintPattern.MatchString(a.CredentialFingerprint) {
		return errors.New("account has an invalid credential fingerprint")
	}
	switch a.State {
	case StateHealthy, StateLowQuota, StateUnknown, StateNeedsLogin, StateDisabled:
	default:
		return fmt.Errorf("account has an invalid state %q", a.State)
	}
	if a.ConsecutiveFailures < 0 || a.ConsecutiveFailures > 1_000_000 {
		return errors.New("account has an invalid failure count")
	}
	if a.Quota != nil {
		if err := a.Quota.Validate(); err != nil {
			return fmt.Errorf("account quota: %w", err)
		}
	}
	return nil
}

func (c Catalog) Validate() error {
	if c.Version != CatalogVersion {
		return fmt.Errorf("unsupported account catalog version %d", c.Version)
	}
	ids := make(map[string]struct{}, len(c.Accounts))
	labels := make(map[string]string, len(c.Accounts))
	for _, account := range c.Accounts {
		if err := account.Validate(); err != nil {
			return fmt.Errorf("validate account %q: %w", account.ID, err)
		}
		if _, duplicate := ids[account.ID]; duplicate {
			return fmt.Errorf("duplicate account ID %s", account.ID)
		}
		ids[account.ID] = struct{}{}
		labelKey := strings.ToLower(account.Label)
		if other, duplicate := labels[labelKey]; duplicate && other != account.ID {
			return errors.New("duplicate account label")
		}
		labels[labelKey] = account.ID
	}
	if c.DefaultAccountID != "" {
		if _, found := ids[c.DefaultAccountID]; !found {
			return errors.New("default account does not exist in catalog")
		}
	}
	return nil
}

func (c *Catalog) Sort() {
	sort.SliceStable(c.Accounts, func(left, right int) bool {
		return c.Accounts[left].ID < c.Accounts[right].ID
	})
}

func (c Catalog) Find(id string) (Account, bool) {
	for _, account := range c.Accounts {
		if account.ID == id {
			return account, true
		}
	}
	return Account{}, false
}

func (t Transaction) Validate() error {
	if t.Version != TransactionVersion {
		return fmt.Errorf("unsupported account transaction version %d", t.Version)
	}
	if !operationIDPattern.MatchString(t.OperationID) {
		return errors.New("account transaction has an invalid operation ID")
	}
	switch t.Kind {
	case TransactionAdd, TransactionSwitch, TransactionProbe, TransactionDeveloperStart, TransactionImport:
	default:
		return fmt.Errorf("account transaction has an invalid kind %q", t.Kind)
	}
	switch t.Phase {
	case PhasePrepared, PhaseCanonicalIsolated, PhaseLoginRunning, PhaseCandidateActive, PhaseValidating, PhaseRestoring, PhaseCommitted:
	default:
		return fmt.Errorf("account transaction has an invalid phase %q", t.Phase)
	}
	if t.StartedAt.IsZero() || t.UpdatedAt.IsZero() || t.UpdatedAt.Before(t.StartedAt) {
		return errors.New("account transaction has invalid timestamps")
	}
	for name, id := range map[string]string{
		"previous default":   t.PreviousDefaultID,
		"previous canonical": t.PreviousCanonicalID,
		"candidate":          t.CandidateID,
	} {
		if id != "" && !accountIDPattern.MatchString(id) {
			return fmt.Errorf("account transaction has invalid %s ID", name)
		}
	}
	if len(t.TargetDeveloper) > 256 {
		return errors.New("account transaction target developer is too long")
	}
	return nil
}
