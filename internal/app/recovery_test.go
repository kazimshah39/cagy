package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kazimshah39/cagy/internal/accounts"
	"github.com/kazimshah39/cagy/internal/herdr"
	"github.com/kazimshah39/cagy/internal/transcript"
)

type recoveryValidator struct {
	byCredential map[string]accounts.ProviderIdentity
	now          time.Time
	fail         map[string]error
}

func (v recoveryValidator) Validate(_ context.Context, credential []byte) (accounts.ValidationResult, error) {
	if err := v.fail[string(credential)]; err != nil {
		return accounts.ValidationResult{}, err
	}
	identity, ok := v.byCredential[string(credential)]
	if !ok {
		return accounts.ValidationResult{}, errors.New("unknown credential")
	}
	return accounts.ValidationResult{
		Identity: identity,
		Quota: accounts.QuotaSnapshot{
			Class:      accounts.QuotaUnknown,
			Reason:     "quota requires live agy probe",
			ObservedAt: v.now,
		},
		Credential: append([]byte(nil), credential...),
	}, nil
}

type recoveryFixture struct {
	app        *App
	service    *accounts.AccountService
	info       runtimeContext
	developer  herdr.AgentInfo
	accounts   []accounts.Account
	stopCount  int
	startIDs   []string
	boundIDs   []string
	prompts    []string
	probeQueue []quotaProbeResult
	runQueue   []developerTaskResult
}

func newRecoveryFixture(t *testing.T, accountCount int) *recoveryFixture {
	t.Helper()
	now := time.Date(2026, time.September, 19, 9, 0, 0, 0, time.UTC)
	repository := accounts.NewRepository(t.TempDir())
	vault := &commandVault{items: map[string][]byte{}}
	canonical := &commandCanonical{isolated: map[string][]byte{}}
	identities := map[string]accounts.ProviderIdentity{}
	catalog := accounts.Catalog{Version: accounts.CatalogVersion, Revision: 1}
	for index := 0; index < accountCount; index++ {
		subject := fmt.Sprintf("recovery-account-%d", index)
		id, err := accounts.StableID(subject)
		if err != nil {
			t.Fatal(err)
		}
		credential := []byte(fmt.Sprintf(`{"token":{"access_token":"access-%d","refresh_token":"refresh-%d"}}`, index, index))
		fingerprint, _ := accounts.CredentialFingerprint(credential)
		weekly, five := .9-float64(index)*.1, .9-float64(index)*.1
		account := accounts.Account{
			ID:                    id,
			Provider:              accounts.ProviderGoogle,
			Label:                 fmt.Sprintf("Account %d", index),
			Email:                 fmt.Sprintf("account%d@example.com", index),
			CredentialFingerprint: fingerprint,
			State:                 accounts.StateHealthy,
			LastVerifiedAt:        now,
			Quota: &accounts.QuotaSnapshot{
				Class:             accounts.QuotaAvailable,
				WeeklyRemaining:   &weekly,
				FiveHourRemaining: &five,
				ObservedAt:        now,
			},
		}
		catalog.Accounts = append(catalog.Accounts, account)
		vault.items[id] = credential
		identities[string(credential)] = accounts.ProviderIdentity{Subject: subject, Email: account.Email}
	}
	if accountCount > 0 {
		catalog.DefaultAccountID = catalog.Accounts[0].ID
		canonical.value = append([]byte(nil), vault.items[catalog.DefaultAccountID]...)
	}
	if err := repository.SaveCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	service := &accounts.AccountService{
		Repository: repository,
		Vault:      vault,
		Canonical:  canonical,
		Validator:  recoveryValidator{byCredential: identities, now: now, fail: map[string]error{}},
		Acquire:    func() (accounts.OperationLock, error) { return commandLock{}, nil },
		Now:        func() time.Time { return now },
		Operation:  func() string { return "recovery-operation" },
	}
	application := New(&quotaProbeRunner{}, &strings.Builder{}, &strings.Builder{})
	application.stateDir = t.TempDir()
	application.now = func() time.Time { return now }
	application.accountsFactory = func() (*accounts.AccountService, error) { return service, nil }
	fixture := &recoveryFixture{
		app:      application,
		service:  service,
		accounts: catalog.Accounts,
		info: runtimeContext{
			workspaceID:   "w1",
			supervisor:    "w1:p1",
			developer:     "developer",
			developerPane: "w1:p2",
			project:       "/tmp/project",
		},
		developer: herdr.AgentInfo{
			Agent:         "agy",
			AgentStatus:   "idle",
			PaneID:        "w1:p2",
			WorkspaceID:   "w1",
			ForegroundCWD: "/tmp/project",
			AgentSession:  &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testConversationID},
		},
	}
	application.recoveryCurrentAccount = func(_ context.Context, _ runtimeContext, _ herdr.AgentInfo, catalog accounts.Catalog) (string, error) {
		return catalog.DefaultAccountID, nil
	}
	application.recoveryStop = func(context.Context, string) error {
		fixture.stopCount++
		return nil
	}
	application.recoveryStart = func(_ context.Context, _ runtimeContext, paneID, sessionID string) (herdr.AgentInfo, error) {
		if paneID != fixture.developer.PaneID || sessionID != testConversationID {
			return herdr.AgentInfo{}, fmt.Errorf("unexpected restart pane=%s session=%s", paneID, sessionID)
		}
		loaded, err := service.Repository.LoadCatalog()
		if err != nil {
			return herdr.AgentInfo{}, err
		}
		fixture.startIDs = append(fixture.startIDs, loaded.DefaultAccountID)
		return fixture.developer, nil
	}
	application.recoveryProbe = func(context.Context, string) quotaProbeResult {
		if len(fixture.probeQueue) == 0 {
			return quotaProbeResult{Class: quotaUnknown, Reason: "missing test probe", ObservedAt: now}
		}
		result := fixture.probeQueue[0]
		fixture.probeQueue = fixture.probeQueue[1:]
		return result
	}
	application.recoveryCheckpoint = func(herdr.AgentInfo) (transcript.Checkpoint, error) {
		return transcript.Checkpoint{SessionID: testConversationID}, nil
	}
	application.recoveryBind = func(_ context.Context, _, _, accountID string) error {
		fixture.boundIDs = append(fixture.boundIDs, accountID)
		return nil
	}
	application.recoveryRunTask = func(_ context.Context, _ string, prompt string, _ transcript.Checkpoint) (developerTaskResult, error) {
		fixture.prompts = append(fixture.prompts, prompt)
		if len(fixture.runQueue) == 0 {
			return developerTaskResult{agent: fixture.developer, output: "done"}, nil
		}
		result := fixture.runQueue[0]
		fixture.runQueue = fixture.runQueue[1:]
		if result.agent.PaneID == "" {
			result.agent = fixture.developer
		}
		return result, nil
	}
	return fixture
}

func availableRecoveryQuota(now time.Time) quotaProbeResult {
	weekly, five := .8, .9
	return quotaProbeResult{Class: quotaAvailable, Reason: "available", WeeklyRemaining: &weekly, FiveHourRemaining: &five, ObservedAt: now}
}

func exhaustedRecoveryQuota(now time.Time) quotaProbeResult {
	weekly, five := 0.0, .9
	return quotaProbeResult{Class: quotaExhausted, Reason: "exhausted", WeeklyRemaining: &weekly, FiveHourRemaining: &five, ObservedAt: now}
}

func TestRecoveryPreflightSwitchesAndSubmitsOriginalOnce(t *testing.T) {
	fixture := newRecoveryFixture(t, 2)
	fixture.probeQueue = []quotaProbeResult{availableRecoveryQuota(fixture.app.now())}
	output, developer, err := fixture.app.recover(context.Background(), fixture.info, fixture.developer, "implement feature", false, exhaustedRecoveryQuota(fixture.app.now()))
	if err != nil {
		t.Fatal(err)
	}
	if output != "done" || developer.PaneID != fixture.developer.PaneID {
		t.Fatalf("output=%q developer=%+v", output, developer)
	}
	if fixture.stopCount != 1 || len(fixture.startIDs) != 1 || fixture.startIDs[0] != fixture.accounts[1].ID {
		t.Fatalf("stops=%d starts=%v", fixture.stopCount, fixture.startIDs)
	}
	if len(fixture.prompts) != 1 || fixture.prompts[0] != "implement feature" {
		t.Fatalf("prompts=%q", fixture.prompts)
	}
	if len(fixture.boundIDs) != 1 || fixture.boundIDs[0] != fixture.accounts[1].ID {
		t.Fatalf("bindings=%v", fixture.boundIDs)
	}
}

func TestRecoveryMidTaskResumesWithContinuationOnly(t *testing.T) {
	fixture := newRecoveryFixture(t, 2)
	fixture.probeQueue = []quotaProbeResult{availableRecoveryQuota(fixture.app.now())}
	output, _, err := fixture.app.recover(context.Background(), fixture.info, fixture.developer, "secret original task", true, exhaustedRecoveryQuota(fixture.app.now()))
	if err != nil {
		t.Fatal(err)
	}
	if output != "done" || len(fixture.prompts) != 1 {
		t.Fatalf("output=%q prompts=%q", output, fixture.prompts)
	}
	if fixture.prompts[0] != continuationPrompt("secret original task") || strings.Contains(fixture.prompts[0], "secret original task") {
		t.Fatalf("continuation=%q", fixture.prompts[0])
	}
}

func TestRecoveryTriesEachAccountOnceAcrossRepeatedExhaustion(t *testing.T) {
	fixture := newRecoveryFixture(t, 3)
	fixture.probeQueue = []quotaProbeResult{availableRecoveryQuota(fixture.app.now()), availableRecoveryQuota(fixture.app.now())}
	fixture.runQueue = []developerTaskResult{
		{quota: exhaustedRecoveryQuota(fixture.app.now())},
		{output: "finished"},
	}
	output, _, err := fixture.app.recover(context.Background(), fixture.info, fixture.developer, "original", false, exhaustedRecoveryQuota(fixture.app.now()))
	if err != nil {
		t.Fatal(err)
	}
	if output != "finished" {
		t.Fatalf("output=%q", output)
	}
	if len(fixture.startIDs) != 2 || fixture.startIDs[0] == fixture.startIDs[1] {
		t.Fatalf("startIDs=%v", fixture.startIDs)
	}
	if len(fixture.prompts) != 2 || fixture.prompts[0] != "original" || fixture.prompts[1] != continuationPrompt("original") {
		t.Fatalf("prompts=%q", fixture.prompts)
	}
	if fixture.stopCount != 2 {
		t.Fatalf("stopCount=%d want=2", fixture.stopCount)
	}
}

func TestRecoveryExhaustedPoolRestoresOriginalDeveloper(t *testing.T) {
	fixture := newRecoveryFixture(t, 3)
	fixture.probeQueue = []quotaProbeResult{exhaustedRecoveryQuota(fixture.app.now()), exhaustedRecoveryQuota(fixture.app.now())}
	_, developer, err := fixture.app.recover(context.Background(), fixture.info, fixture.developer, "original", true, exhaustedRecoveryQuota(fixture.app.now()))
	if err == nil || !strings.Contains(err.Error(), "no healthy stored agy account") {
		t.Fatalf("error=%v", err)
	}
	if developer.PaneID != fixture.developer.PaneID {
		t.Fatalf("developer=%+v", developer)
	}
	loaded, loadErr := fixture.service.Repository.LoadCatalog()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if loaded.DefaultAccountID != fixture.accounts[0].ID {
		t.Fatalf("default=%s want=%s", loaded.DefaultAccountID, fixture.accounts[0].ID)
	}
	if got := fixture.startIDs[len(fixture.startIDs)-1]; got != fixture.accounts[0].ID {
		t.Fatalf("last start=%s want original=%s starts=%v", got, fixture.accounts[0].ID, fixture.startIDs)
	}
	if got := fixture.boundIDs[len(fixture.boundIDs)-1]; got != fixture.accounts[0].ID {
		t.Fatalf("last binding=%s want original=%s", got, fixture.accounts[0].ID)
	}
}

func TestRecoveryRestartFailureRestoresOriginalWithoutTryingEveryAccount(t *testing.T) {
	fixture := newRecoveryFixture(t, 4)
	startCalls := 0
	fixture.app.recoveryStart = func(_ context.Context, _ runtimeContext, paneID, sessionID string) (herdr.AgentInfo, error) {
		if paneID != fixture.developer.PaneID || sessionID != testConversationID {
			return herdr.AgentInfo{}, fmt.Errorf("unexpected restart pane=%s session=%s", paneID, sessionID)
		}
		loaded, err := fixture.service.Repository.LoadCatalog()
		if err != nil {
			return herdr.AgentInfo{}, err
		}
		fixture.startIDs = append(fixture.startIDs, loaded.DefaultAccountID)
		startCalls++
		if startCalls == 1 {
			return herdr.AgentInfo{}, errors.New("simulated pane startup failure")
		}
		return fixture.developer, nil
	}

	_, developer, err := fixture.app.recover(context.Background(), fixture.info, fixture.developer, "original", false, exhaustedRecoveryQuota(fixture.app.now()))
	if err == nil || !strings.Contains(err.Error(), "original developer was restored") {
		t.Fatalf("error=%v", err)
	}
	if developer.PaneID != fixture.developer.PaneID {
		t.Fatalf("developer=%+v", developer)
	}
	if len(fixture.startIDs) != 2 {
		t.Fatalf("starts=%v, want failed candidate plus one original restore", fixture.startIDs)
	}
	if fixture.startIDs[0] != fixture.accounts[1].ID || fixture.startIDs[1] != fixture.accounts[0].ID {
		t.Fatalf("starts=%v", fixture.startIDs)
	}
	loaded, loadErr := fixture.service.Repository.LoadCatalog()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if loaded.DefaultAccountID != fixture.accounts[0].ID {
		t.Fatalf("default=%s want original=%s", loaded.DefaultAccountID, fixture.accounts[0].ID)
	}
	for _, account := range loaded.Accounts[2:] {
		if !account.LastFailureAt.IsZero() {
			t.Fatalf("untried account %s was incorrectly marked failed", account.ID)
		}
	}
}

func TestRecoveryWithoutResolvedCurrentAccountStillTriesStoredAccounts(t *testing.T) {
	fixture := newRecoveryFixture(t, 1)
	fixture.app.recoveryCurrentAccount = func(context.Context, runtimeContext, herdr.AgentInfo, accounts.Catalog) (string, error) {
		return "", nil
	}
	fixture.probeQueue = []quotaProbeResult{availableRecoveryQuota(fixture.app.now())}

	output, _, err := fixture.app.recover(context.Background(), fixture.info, fixture.developer, "original", false, exhaustedRecoveryQuota(fixture.app.now()))
	if err != nil {
		t.Fatal(err)
	}
	if output != "done" || fixture.stopCount != 1 || len(fixture.startIDs) != 1 {
		t.Fatalf("output=%q stops=%d starts=%v", output, fixture.stopCount, fixture.startIDs)
	}
}

func TestRecoveryWithNoCandidateDoesNotStopDeveloper(t *testing.T) {
	fixture := newRecoveryFixture(t, 1)
	trackRecoveryTask(t, fixture, "original")
	_, _, err := fixture.app.recover(context.Background(), fixture.info, fixture.developer, "original", false, exhaustedRecoveryQuota(fixture.app.now()))
	if err == nil || !strings.Contains(err.Error(), "no healthy stored agy account") {
		t.Fatalf("error=%v", err)
	}
	if fixture.stopCount != 0 || len(fixture.startIDs) != 0 || len(fixture.prompts) != 0 {
		t.Fatalf("stops=%d starts=%v prompts=%v", fixture.stopCount, fixture.startIDs, fixture.prompts)
	}
	record, exists, loadErr := fixture.app.loadTaskJournal(fixture.info.developer)
	if loadErr != nil || !exists {
		t.Fatalf("exists=%v err=%v", exists, loadErr)
	}
	if record.FailureCode != taskFailureQuotaAccountsUnavailable || record.Phase != taskPhaseUncertain {
		t.Fatalf("record=%+v", record)
	}
}

func TestRecoveryDiagnosticsDescribeFlowWithoutTaskAnswerEmailOrSecrets(t *testing.T) {
	fixture := newRecoveryFixture(t, 2)
	secretTask := "PRIVATE-TASK-DO-NOT-LOG"
	secretAnswer := "PRIVATE-ANSWER-DO-NOT-LOG"
	secretToken := "access_token=PRIVATE-TOKEN-DO-NOT-LOG"
	fixture.probeQueue = []quotaProbeResult{availableRecoveryQuota(fixture.app.now())}
	fixture.runQueue = []developerTaskResult{{output: secretAnswer}}
	var diagnostics []string
	fixture.app.diagnosticSink = func(message string) { diagnostics = append(diagnostics, message) }
	output, _, err := fixture.app.recover(context.Background(), fixture.info, fixture.developer, secretTask, false, exhaustedRecoveryQuota(fixture.app.now()))
	if err != nil {
		t.Fatal(err)
	}
	if output != secretAnswer {
		t.Fatalf("output=%q", output)
	}
	text := strings.Join(diagnostics, "\n")
	for _, expected := range []string{"recovery begin", "recovery candidates", "recovery attempt", "recovery candidate-quota"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing diagnostic %q in %s", expected, text)
		}
	}
	for _, forbidden := range []string{secretTask, secretAnswer, secretToken, fixture.accounts[1].Email} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("diagnostics leaked %q: %s", forbidden, text)
		}
	}
}

func TestRecoveryPreflightAllowsFreshPendingSession(t *testing.T) {
	fixture := newRecoveryFixture(t, 2)
	fixture.developer.AgentSession = nil
	fixture.probeQueue = []quotaProbeResult{availableRecoveryQuota(fixture.app.now())}
	fixture.app.recoveryStart = func(_ context.Context, _ runtimeContext, paneID, sessionID string) (herdr.AgentInfo, error) {
		if paneID != fixture.developer.PaneID || sessionID != "" {
			return herdr.AgentInfo{}, fmt.Errorf("unexpected fresh restart pane=%s session=%s", paneID, sessionID)
		}
		started := fixture.developer
		started.AgentSession = &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testConversationID}
		return started, nil
	}
	output, _, err := fixture.app.recover(context.Background(), fixture.info, fixture.developer, "first task", false, exhaustedRecoveryQuota(fixture.app.now()))
	if err != nil {
		t.Fatal(err)
	}
	if output != "done" || len(fixture.prompts) != 1 || fixture.prompts[0] != "first task" {
		t.Fatalf("output=%q prompts=%v", output, fixture.prompts)
	}
}

func TestRecoveryMidTaskRejectsMissingConversationIdentity(t *testing.T) {
	fixture := newRecoveryFixture(t, 2)
	fixture.developer.AgentSession = nil
	_, _, err := fixture.app.recover(context.Background(), fixture.info, fixture.developer, "started task", true, exhaustedRecoveryQuota(fixture.app.now()))
	if err == nil || !strings.Contains(err.Error(), "exact agy conversation") {
		t.Fatalf("error=%v", err)
	}
	if fixture.stopCount != 0 {
		t.Fatalf("developer was stopped without exact session: %d", fixture.stopCount)
	}
}

func TestCurrentAccountForRecoveryPrefersVerifiedPaneBinding(t *testing.T) {
	fixture := newRecoveryFixture(t, 2)
	boundID := fixture.accounts[1].ID
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "pane", "get", fixture.developer.PaneID}, result: paneJSON(fixture.developer.PaneID, "w1:t1", fixture.info.project, developerPaneLabel, map[string]string{"cagy_account_id": boundID})},
	}}
	application := New(runner, &strings.Builder{}, &strings.Builder{})
	catalog, err := fixture.service.Repository.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	got, err := application.currentAccountForRecovery(context.Background(), fixture.info, fixture.developer, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if got != boundID {
		t.Fatalf("account=%s want=%s", got, boundID)
	}
	runner.assertDone()
}

func TestCurrentAccountForRecoveryRejectsBindingOutsideCatalog(t *testing.T) {
	fixture := newRecoveryFixture(t, 1)
	unknownID, _ := accounts.StableID("not-in-catalog")
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "pane", "get", fixture.developer.PaneID}, result: paneJSON(fixture.developer.PaneID, "w1:t1", fixture.info.project, developerPaneLabel, map[string]string{"cagy_account_id": unknownID})},
	}}
	application := New(runner, &strings.Builder{}, &strings.Builder{})
	catalog, _ := fixture.service.Repository.LoadCatalog()
	_, err := application.currentAccountForRecovery(context.Background(), fixture.info, fixture.developer, catalog)
	if err == nil || !strings.Contains(err.Error(), "not present") {
		t.Fatalf("error=%v", err)
	}
	runner.assertDone()
}

func trackRecoveryTask(t *testing.T, fixture *recoveryFixture, task string) {
	t.Helper()
	if err := fixture.app.beginTaskTracking(fixture.info, fixture.developer, task, transcript.Checkpoint{SessionID: testConversationID}, taskPhaseMonitoring); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryStopTimeoutPersistsSafeFailureReason(t *testing.T) {
	fixture := newRecoveryFixture(t, 2)
	trackRecoveryTask(t, fixture, "stop timeout task")
	fixture.app.recoveryStop = func(context.Context, string) error {
		return errAgentReleaseTimeout
	}

	_, _, err := fixture.app.recover(context.Background(), fixture.info, fixture.developer, "stop timeout task", true, exhaustedRecoveryQuota(fixture.app.now()))
	if err == nil || !errors.Is(err, errAgentReleaseTimeout) {
		t.Fatalf("error=%v", err)
	}
	record, exists, loadErr := fixture.app.loadTaskJournal(fixture.info.developer)
	if loadErr != nil || !exists {
		t.Fatalf("exists=%v err=%v", exists, loadErr)
	}
	if record.Phase != taskPhaseUncertain || record.FailureCode != taskFailureQuotaRecoveryStopTimeout || record.FailureAt.IsZero() {
		t.Fatalf("record=%+v", record)
	}
}

func TestRecoveryExhaustedPoolPersistsOriginalRestoredReason(t *testing.T) {
	fixture := newRecoveryFixture(t, 2)
	trackRecoveryTask(t, fixture, "exhausted pool task")
	fixture.probeQueue = []quotaProbeResult{exhaustedRecoveryQuota(fixture.app.now())}

	_, _, err := fixture.app.recover(context.Background(), fixture.info, fixture.developer, "exhausted pool task", true, exhaustedRecoveryQuota(fixture.app.now()))
	if err == nil || !strings.Contains(err.Error(), "original developer was restored") {
		t.Fatalf("error=%v", err)
	}
	record, exists, loadErr := fixture.app.loadTaskJournal(fixture.info.developer)
	if loadErr != nil || !exists {
		t.Fatalf("exists=%v err=%v", exists, loadErr)
	}
	if record.Phase != taskPhaseUncertain || record.FailureCode != taskFailureQuotaAccountsUnavailable || record.FailureAt.IsZero() {
		t.Fatalf("record=%+v", record)
	}
}

func TestRecoveryExhaustedPoolPersistsOriginalRestoreFailure(t *testing.T) {
	fixture := newRecoveryFixture(t, 2)
	trackRecoveryTask(t, fixture, "restore failure task")
	fixture.probeQueue = []quotaProbeResult{exhaustedRecoveryQuota(fixture.app.now())}
	startCalls := 0
	fixture.app.recoveryStart = func(_ context.Context, _ runtimeContext, paneID, sessionID string) (herdr.AgentInfo, error) {
		if paneID != fixture.developer.PaneID || sessionID != testConversationID {
			return herdr.AgentInfo{}, fmt.Errorf("unexpected restart pane=%s session=%s", paneID, sessionID)
		}
		startCalls++
		if startCalls == 2 {
			return herdr.AgentInfo{}, errors.New("simulated original restart failure")
		}
		return fixture.developer, nil
	}

	_, _, err := fixture.app.recover(context.Background(), fixture.info, fixture.developer, "restore failure task", true, exhaustedRecoveryQuota(fixture.app.now()))
	if err == nil || !strings.Contains(err.Error(), "original developer could not be restored") {
		t.Fatalf("error=%v", err)
	}
	record, exists, loadErr := fixture.app.loadTaskJournal(fixture.info.developer)
	if loadErr != nil || !exists {
		t.Fatalf("exists=%v err=%v", exists, loadErr)
	}
	if record.Phase != taskPhaseUncertain || record.FailureCode != taskFailureQuotaOriginalRestoreFailed || record.FailureAt.IsZero() {
		t.Fatalf("record=%+v", record)
	}
}

func TestRecoveryGlobalRefreshFailureRestartsUnchangedOriginalOnce(t *testing.T) {
	fixture := newRecoveryFixture(t, 3)
	trackRecoveryTask(t, fixture, "global refresh failure task")
	fixture.service.Validator = rotationValidatorFunc(func(context.Context, []byte) (accounts.ValidationResult, error) {
		return accounts.ValidationResult{}, fmt.Errorf("temporary refresh outage: %w", accounts.ErrCredentialRefreshUnavailable)
	})

	_, developer, err := fixture.app.recover(context.Background(), fixture.info, fixture.developer, "global refresh failure task", true, exhaustedRecoveryQuota(fixture.app.now()))
	if err == nil || !accounts.CredentialRefreshUnavailable(err) || !strings.Contains(err.Error(), "original developer was restored") {
		t.Fatalf("developer=%+v error=%v", developer, err)
	}
	if len(fixture.startIDs) != 1 || fixture.startIDs[0] != fixture.accounts[0].ID {
		t.Fatalf("starts=%v", fixture.startIDs)
	}
	if len(fixture.boundIDs) != 1 || fixture.boundIDs[0] != fixture.accounts[0].ID {
		t.Fatalf("bindings=%v", fixture.boundIDs)
	}
	record, exists, loadErr := fixture.app.loadTaskJournal(fixture.info.developer)
	if loadErr != nil || !exists {
		t.Fatalf("exists=%v err=%v", exists, loadErr)
	}
	if record.FailureCode != taskFailureQuotaRefreshUnavailable || record.Phase != taskPhaseUncertain {
		t.Fatalf("record=%+v", record)
	}
	loaded, loadErr := fixture.service.Repository.LoadCatalog()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	for _, account := range loaded.Accounts[1:] {
		if account.ConsecutiveFailures != 0 || !account.LastFailureAt.IsZero() {
			t.Fatalf("global failure incorrectly penalized account: %+v", account)
		}
	}
}
