// Package health runs the periodic account health check and the auto-switch
// decision engine. Every N seconds it polls each sub2api instance's accounts,
// evaluates them, and — when an account degrades past the fail threshold —
// automatically takes it out of scheduling and brings a healthy backup in
// (L1, audited), then notifies. Snapshots are kept in memory for the console.
package health

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/notify"
	"e2m.local/core/internal/orchestrator"
	"e2m.local/core/internal/store"
)

// Config tunes the checker.
type Config struct {
	Interval   time.Duration // poll period (default 60s)
	FailStreak int           // consecutive bad checks before auto-switch (default 2)
	Cooldown   time.Duration // don't auto-switch the same account twice within this (default 5m)
	AutoSwitch bool          // fallback per-instance preference for direct/legacy callers
	// AllowLegacyAutoSwitch is the process-wide safety gate for this deprecated
	// write path. It defaults false and cannot be enabled by an instance policy.
	// Monitoring, snapshots, alerts and drift detection continue while disabled.
	AllowLegacyAutoSwitch bool
	KindsChecked          []contracts.InstanceKind

	// BalanceThreshold triggers a low-balance alert when an account reports a
	// balance below it. 0 disables balance alerting. Alerts repeat no more often
	// than BalanceAlertCooldown per account and re-arm when the balance recovers.
	BalanceThreshold     float64
	BalanceAlertCooldown time.Duration // default 1h
	// DriftDetection compares each account's upstream state (status /
	// schedulable / priority, plus appearing/disappearing accounts) against the
	// previous check and audits + notifies on changes E2M didn't make itself.
	DriftDetection bool

	// Strategy selects how a backup account is scored during auto-switch
	// ("stability" | "cost" | "performance"). Empty defaults to stability.
	Strategy string
}

func (c Config) withDefaults() Config {
	if c.Interval <= 0 {
		c.Interval = 60 * time.Second
	}
	if c.FailStreak <= 0 {
		c.FailStreak = 2
	}
	if c.Cooldown <= 0 {
		c.Cooldown = 5 * time.Minute
	}
	if c.BalanceAlertCooldown <= 0 {
		c.BalanceAlertCooldown = time.Hour
	}
	if len(c.KindsChecked) == 0 {
		c.KindsChecked = []contracts.InstanceKind{
			contracts.InstanceKindSub2API,
			contracts.InstanceKindNewAPI,
			contracts.InstanceKindCPA,
		}
	}
	return c
}

// EventSink receives console-facing realtime events (SSE bridge). Nil is fine.
type EventSink func(eventType string, userID int64, payload any)

// Checker polls instances and drives auto-switching.
type Checker struct {
	cfg      Config
	strategy SwitchStrategy
	store    store.Store
	orch     *orchestrator.Orchestrator
	router   *notify.Router
	now      func() time.Time
	events   EventSink

	mu            sync.RWMutex
	snapshots     map[string]contracts.InstanceHealthSnapshot    // instanceID -> latest
	failStreaks   map[string]int                                 // instanceID|accountID -> streak
	lastSwitched  map[string]time.Time                           // instanceID|accountID -> last auto-switch
	balanceAlerts map[string]time.Time                           // instanceID|accountID -> last low-balance alert
	prevAccounts  map[string]map[string]contracts.GatewayAccount // instanceID -> accountID -> last-seen state
	lastChecks    map[string]time.Time                           // instanceID -> last scheduled check
	runningChecks map[string]struct{}                            // instanceID -> in-flight check
}

func New(cfg Config, st store.Store, orch *orchestrator.Orchestrator, router *notify.Router) *Checker {
	return &Checker{
		cfg:           cfg.withDefaults(),
		strategy:      normalizeStrategy(cfg.Strategy),
		store:         st,
		orch:          orch,
		router:        router,
		now:           time.Now,
		snapshots:     map[string]contracts.InstanceHealthSnapshot{},
		failStreaks:   map[string]int{},
		lastSwitched:  map[string]time.Time{},
		balanceAlerts: map[string]time.Time{},
		prevAccounts:  map[string]map[string]contracts.GatewayAccount{},
		lastChecks:    map[string]time.Time{},
		runningChecks: map[string]struct{}{},
	}
}

// SetEventSink wires the console SSE bridge. Call before Run.
func (c *Checker) SetEventSink(sink EventSink) { c.events = sink }

func (c *Checker) emit(eventType string, userID int64, payload any) {
	if c.events != nil {
		c.events(eventType, userID, payload)
	}
}

// Run blocks, ticking until ctx is cancelled. Start it in a goroutine.
func (c *Checker) Run(ctx context.Context) {
	log.Printf("health checker started (scheduler=5s default_interval=%s)", c.cfg.Interval)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	c.checkAll(ctx) // run once immediately
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.checkAll(ctx)
		}
	}
}

// CheckNow performs an immediate check without changing the stored cadence.
func (c *Checker) CheckNow(ctx context.Context, instanceID string) (contracts.InstanceHealthSnapshot, error) {
	instance, err := c.store.GetInstance(ctx, instanceID)
	if err != nil {
		return contracts.InstanceHealthSnapshot{}, err
	}
	if !c.checks(instance.Kind) {
		return contracts.InstanceHealthSnapshot{}, fmt.Errorf("health: instance kind %s is not supported", instance.Kind)
	}
	policy, err := c.store.GetInstanceMonitorPolicy(ctx, instanceID)
	if err != nil {
		return contracts.InstanceHealthSnapshot{}, err
	}
	if !c.startCheck(instance.ID) {
		return contracts.InstanceHealthSnapshot{}, fmt.Errorf("health: instance check is already running")
	}
	defer c.finishCheck(instance.ID)
	return c.checkInstance(ctx, instance, policy), nil
}

// Snapshots returns the latest per-instance health, optionally filtered by id.
func (c *Checker) Snapshots(instanceID string) []contracts.InstanceHealthSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]contracts.InstanceHealthSnapshot, 0, len(c.snapshots))
	for id, s := range c.snapshots {
		if instanceID == "" || id == instanceID {
			out = append(out, s)
		}
	}
	return out
}

func (c *Checker) checkAll(ctx context.Context) {
	instances, err := c.store.ListInstances(ctx, 0)
	if err != nil {
		log.Printf("health: list instances: %v", err)
		return
	}
	for _, inst := range instances {
		if !c.checks(inst.Kind) {
			continue
		}
		policy, err := c.store.GetInstanceMonitorPolicy(ctx, inst.ID)
		if err != nil {
			log.Printf("health: load policy for %s: %v", inst.ID, err)
			continue
		}
		if !policy.Enabled || !c.checkDue(inst.ID, time.Duration(policy.CheckIntervalSeconds)*time.Second) {
			continue
		}
		if !c.startCheck(inst.ID) {
			continue
		}
		func() {
			defer c.finishCheck(inst.ID)
			c.checkInstance(ctx, inst, policy)
		}()
	}
}

func (c *Checker) startCheck(instanceID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, running := c.runningChecks[instanceID]; running {
		return false
	}
	c.runningChecks[instanceID] = struct{}{}
	return true
}

func (c *Checker) finishCheck(instanceID string) {
	c.mu.Lock()
	delete(c.runningChecks, instanceID)
	c.mu.Unlock()
}

func (c *Checker) checkDue(instanceID string, interval time.Duration) bool {
	if interval <= 0 {
		interval = c.cfg.Interval
	}
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	last, ok := c.lastChecks[instanceID]
	if ok && now.Sub(last) < interval {
		return false
	}
	c.lastChecks[instanceID] = now
	return true
}

func (c *Checker) checks(kind contracts.InstanceKind) bool {
	for _, k := range c.cfg.KindsChecked {
		if k == kind {
			return true
		}
	}
	return false
}

func (c *Checker) checkInstance(ctx context.Context, inst contracts.Instance, policies ...contracts.InstanceMonitorPolicy) contracts.InstanceHealthSnapshot {
	policy := contracts.DefaultInstanceMonitorPolicy(inst.ID, inst.UserID)
	policy.AutoSwitch = c.cfg.AutoSwitch
	policy.FailStreak = c.cfg.FailStreak
	policy.CooldownSeconds = int(c.cfg.Cooldown / time.Second)
	policy.DriftDetection = c.cfg.DriftDetection
	if len(policies) > 0 {
		policy = policies[0]
	}
	accounts, err := c.orch.ListAccounts(ctx, inst.ID)
	snap := contracts.InstanceHealthSnapshot{
		InstanceID:   inst.ID,
		InstanceName: inst.Name,
		UserID:       inst.UserID,
		CheckedAt:    c.now().UTC(),
	}
	if err != nil {
		snap.LastError = err.Error()
		c.storeSnapshot(snap)
		return snap
	}
	ownerAccounts, classificationErr := c.ownerManagedAccountView(ctx, inst.ID, accounts)
	if classificationErr != nil {
		// Managed IDs must never enter legacy account alerts or writers when the
		// ownership ledger cannot be read. The detailed snapshot remains an
		// admin-only internal fact; owner HTTP responses independently fail closed.
		log.Printf("health: classify managed accounts for %s: %v", inst.ID, classificationErr)
	}

	for _, ac := range accounts {
		healthy := accountHealthy(ac)
		streakKey := inst.ID + "|" + ac.ID
		if healthy {
			c.setStreak(streakKey, 0)
		} else {
			c.bumpStreak(streakKey)
		}
		streak := c.getStreak(streakKey)

		snap.TotalAccounts++
		if healthy {
			snap.HealthyCount++
		}
		if ac.Schedulable {
			snap.Schedulable++
		}
		snap.Accounts = append(snap.Accounts, contracts.AccountHealth{
			AccountID:   ac.ID,
			DisplayName: ac.DisplayName,
			Status:      ac.Status,
			Schedulable: ac.Schedulable,
			Healthy:     healthy,
			FailStreak:  streak,
		})
	}

	// The per-instance preference cannot enable this deprecated writer by itself.
	// Normal monitoring and observations continue with the process-wide gate off.
	if classificationErr == nil {
		if c.legacyAutoSwitchEnabled(policy) {
			if note := c.maybeAutoSwitch(ctx, inst, ownerAccounts, policy); note != "" {
				snap.AutoSwitchNote = note
			}
		}

		// Legacy account-level alerts and drift facts are owner-facing. Platform-
		// managed bindings have their own anonymous quality/circuit surfaces.
		if c.cfg.BalanceThreshold > 0 {
			c.checkBalances(ctx, inst, ownerAccounts)
		}
		if policy.DriftDetection {
			c.detectDrift(ctx, inst, ownerAccounts)
		}
		c.rememberAccounts(inst.ID, ownerAccounts)
	}

	c.storeSnapshot(snap)
	c.emit("health.snapshot", inst.UserID, snap)
	return snap
}

// ownerManagedAccountView removes every gateway account represented by a
// PublishedBinding.RemoteID. All binding states count because a disabled or
// revoked binding can remain remotely present while cleanup is pending.
func (c *Checker) ownerManagedAccountView(ctx context.Context, instanceID string, accounts []contracts.GatewayAccount) ([]contracts.GatewayAccount, error) {
	bindings, err := c.store.ListPublishedBindings(ctx, "")
	if err != nil {
		return nil, err
	}
	managed := map[string]struct{}{}
	for _, binding := range bindings {
		remoteID := strings.TrimSpace(binding.RemoteID)
		if remoteID == "" {
			continue
		}
		bindingInstanceID := strings.TrimSpace(binding.InstanceID)
		if bindingInstanceID == "" {
			plan, err := c.store.GetRoutePlan(ctx, binding.PlanID)
			if err != nil {
				return nil, err
			}
			bindingInstanceID = plan.InstanceID
		}
		if bindingInstanceID == instanceID {
			managed[remoteID] = struct{}{}
		}
	}
	filtered := make([]contracts.GatewayAccount, 0, len(accounts))
	for _, account := range accounts {
		if _, hidden := managed[strings.TrimSpace(account.ID)]; hidden {
			continue
		}
		filtered = append(filtered, account)
	}
	return filtered, nil
}

func (c *Checker) legacyAutoSwitchEnabled(policy contracts.InstanceMonitorPolicy) bool {
	return c.cfg.AllowLegacyAutoSwitch && policy.AutoSwitch
}

// checkBalances alerts when an account's reported balance drops below the
// threshold. Per-account cooldown; re-arms once the balance recovers.
func (c *Checker) checkBalances(ctx context.Context, inst contracts.Instance, accounts []contracts.GatewayAccount) {
	for _, ac := range accounts {
		if ac.Balance == nil {
			continue
		}
		key := inst.ID + "|" + ac.ID
		if *ac.Balance >= c.cfg.BalanceThreshold {
			// Balance is fine again: clear the cooldown so the next dip alerts.
			c.mu.Lock()
			delete(c.balanceAlerts, key)
			c.mu.Unlock()
			continue
		}
		c.mu.Lock()
		last, alerted := c.balanceAlerts[key]
		inCooldown := alerted && c.now().Sub(last) < c.cfg.BalanceAlertCooldown
		if !inCooldown {
			c.balanceAlerts[key] = c.now()
		}
		c.mu.Unlock()
		if inCooldown {
			continue
		}

		text := fmt.Sprintf("账号 %s 余额 %.2f，低于阈值 %.2f，请及时充值或切换供给",
			accountLabel(ac), *ac.Balance, c.cfg.BalanceThreshold)
		_, _ = c.store.AppendAudit(ctx, contracts.OperationAudit{
			UserID:       inst.UserID,
			InstanceID:   inst.ID,
			ActorType:    "system",
			ActorID:      "health-checker",
			Action:       "account.balance_low",
			RiskLevel:    contracts.RiskLevelL0,
			EventLevel:   contracts.EventLevelWarning,
			TargetType:   "account",
			TargetID:     ac.ID,
			Result:       "detected",
			ErrorMessage: text,
		})
		c.dispatch(ctx, inst, contracts.RiskLevelL0, contracts.EventLevelWarning, "detected", "💰 余额预警 · "+inst.Name, text)
		c.emit("account.balance_low", inst.UserID, map[string]any{
			"instance_id": inst.ID, "account_id": ac.ID, "balance": *ac.Balance,
			"threshold": c.cfg.BalanceThreshold,
		})
	}
}

// detectDrift diffs the account set against the previous check and records
// upstream-side changes (status flips, schedulable toggles E2M didn't do,
// priority changes, accounts appearing/disappearing) as audit facts.
func (c *Checker) detectDrift(ctx context.Context, inst contracts.Instance, accounts []contracts.GatewayAccount) {
	c.mu.RLock()
	prev, seen := c.prevAccounts[inst.ID]
	c.mu.RUnlock()
	if !seen {
		return // first observation: nothing to diff against
	}

	var changes []string
	current := map[string]bool{}
	for _, ac := range accounts {
		current[ac.ID] = true
		old, ok := prev[ac.ID]
		if !ok {
			changes = append(changes, fmt.Sprintf("新增账号 %s（status=%s schedulable=%v）", accountLabel(ac), ac.Status, ac.Schedulable))
			continue
		}
		if old.Status != ac.Status {
			changes = append(changes, fmt.Sprintf("账号 %s 状态 %s → %s", accountLabel(ac), orUnknown(old.Status), orUnknown(ac.Status)))
		}
		if old.Schedulable != ac.Schedulable {
			changes = append(changes, fmt.Sprintf("账号 %s 调度 %v → %v", accountLabel(ac), old.Schedulable, ac.Schedulable))
		}
		if old.Priority != ac.Priority {
			changes = append(changes, fmt.Sprintf("账号 %s 优先级 %d → %d", accountLabel(ac), old.Priority, ac.Priority))
		}
	}
	for id, old := range prev {
		if !current[id] {
			changes = append(changes, fmt.Sprintf("账号 %s 从上游消失", accountLabel(old)))
		}
	}
	if len(changes) == 0 {
		return
	}

	text := strings.Join(changes, "\n")
	_, _ = c.store.AppendAudit(ctx, contracts.OperationAudit{
		UserID:       inst.UserID,
		InstanceID:   inst.ID,
		ActorType:    "system",
		ActorID:      "health-checker",
		Action:       "instance.config_drift",
		RiskLevel:    contracts.RiskLevelL0,
		EventLevel:   contracts.EventLevelWarning,
		TargetType:   "instance",
		TargetID:     inst.ID,
		Result:       "detected",
		ErrorMessage: text,
	})
	c.dispatch(ctx, inst, contracts.RiskLevelL0, contracts.EventLevelWarning, "detected", "🔍 配置漂移 · "+inst.Name, text)
	c.emit("instance.config_drift", inst.UserID, map[string]any{
		"instance_id": inst.ID, "changes": changes,
	})
}

// rememberAccounts stores the current account states for the next drift diff.
func (c *Checker) rememberAccounts(instanceID string, accounts []contracts.GatewayAccount) {
	m := make(map[string]contracts.GatewayAccount, len(accounts))
	for _, ac := range accounts {
		m[ac.ID] = ac
	}
	c.mu.Lock()
	c.prevAccounts[instanceID] = m
	c.mu.Unlock()
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// maybeAutoSwitch finds an account that is unhealthy, still schedulable, and past
// the fail-streak threshold + cooldown, then disables it and enables a healthy
// backup. If no such account exists but the pool has ZERO healthy scheduled
// accounts (e.g. new-api self-disabled its channels), it performs an emergency
// spare enable instead. Returns a human note if it acted.
func (c *Checker) maybeAutoSwitch(ctx context.Context, inst contracts.Instance, accounts []contracts.GatewayAccount, policy contracts.InstanceMonitorPolicy) string {
	if !c.legacyAutoSwitchEnabled(policy) {
		return ""
	}
	var problem *contracts.GatewayAccount
	healthyScheduled := 0
	for i := range accounts {
		ac := accounts[i]
		if accountHealthy(ac) && ac.Schedulable {
			healthyScheduled++
		}
		if accountHealthy(ac) || !ac.Schedulable {
			continue
		}
		key := inst.ID + "|" + ac.ID
		if c.getStreak(key) < policy.FailStreak {
			continue
		}
		if c.inCooldown(key, time.Duration(policy.CooldownSeconds)*time.Second) {
			continue
		}
		problem = &accounts[i]
	}
	if problem == nil {
		// Emergency path: the pool has no healthy scheduled account left (the
		// gateway may have self-disabled them, as new-api does with status 3).
		// Debounced like the switch path: the condition must persist FailStreak
		// consecutive checks before we act.
		emptyKey := inst.ID + "|__empty__"
		if healthyScheduled == 0 {
			c.bumpStreak(emptyKey)
			if c.getStreak(emptyKey) >= policy.FailStreak {
				return c.emergencyEnableSpare(ctx, inst, accounts, policy)
			}
		} else {
			c.setStreak(emptyKey, 0)
		}
		return ""
	}

	pick := selectBackup(accounts, problem.ID, problem, c.strategy, c.backupSignals(inst.ID, accounts))
	reason := fmt.Sprintf("自动切换：账号 %s 连续 %d 次异常", accountLabel(*problem), c.getStreak(inst.ID+"|"+problem.ID))
	if pick != nil {
		if r := pick.reason(); r != "" {
			reason += fmt.Sprintf("；选备[%s]依据：%s", accountLabel(pick.account), r)
		}
	}
	sw := contracts.AccountSwitch{
		InstanceID:       inst.ID,
		DisableAccountID: problem.ID,
		Reason:           reason,
	}
	if pick != nil {
		sw.EnableAccountID = pick.account.ID
	}

	err := c.orch.SwitchUpstream(ctx, sw)
	c.markSwitched(inst.ID + "|" + problem.ID)

	note := fmt.Sprintf("已自动停用 %s", accountLabel(*problem))
	if pick != nil {
		note += fmt.Sprintf("，启用备用 %s", accountLabel(pick.account))
		if r := pick.reason(); r != "" {
			note += fmt.Sprintf("（%s）", r)
		}
	} else {
		note += "（无健康备用账号可用）"
	}
	if err != nil {
		note = "自动切换失败：" + err.Error()
	}

	c.notify(ctx, inst, note, err)
	return note
}

// emergencyEnableSpare handles the "no healthy scheduled account at all" state:
// enable one healthy spare so traffic can flow again. Rate-limited per instance.
func (c *Checker) emergencyEnableSpare(ctx context.Context, inst contracts.Instance, accounts []contracts.GatewayAccount, policy contracts.InstanceMonitorPolicy) string {
	if !c.legacyAutoSwitchEnabled(policy) {
		return ""
	}
	pick := selectBackup(accounts, "", nil, c.strategy, c.backupSignals(inst.ID, accounts))
	if pick == nil {
		return "" // nothing healthy to enable; nothing we can do
	}
	spare := pick.account
	instKey := inst.ID + "|__emergency__"
	if c.inCooldown(instKey, time.Duration(policy.CooldownSeconds)*time.Second) {
		return ""
	}
	reason := "自动切换：池内已无健康可调度账号，紧急启用备用"
	if r := pick.reason(); r != "" {
		reason += "；依据：" + r
	}
	err := c.orch.SetSchedulable(ctx, inst.ID, spare.ID, true, reason)
	c.markSwitched(instKey)
	note := fmt.Sprintf("池内已无健康可调度账号，已紧急启用备用 %s", accountLabel(spare))
	if err != nil {
		note = "紧急启用备用失败：" + err.Error()
	}
	c.notify(ctx, inst, note, err)
	return note
}

func (c *Checker) notify(ctx context.Context, inst contracts.Instance, note string, opErr error) {
	title := "⚠️ E2M 自动切换 · " + inst.Name
	eventLevel := contracts.EventLevelNotice
	result := "accepted"
	if opErr != nil {
		title = "❌ E2M 自动切换失败 · " + inst.Name
		eventLevel = contracts.EventLevelWarning
		result = "failed"
	}
	c.dispatch(ctx, inst, contracts.RiskLevelL1, eventLevel, result, title, note)
	c.emit("health.auto_switch", inst.UserID, map[string]any{
		"instance_id": inst.ID, "note": note, "failed": opErr != nil,
	})
}

// dispatch routes one alert through the user's notification routes.
func (c *Checker) dispatch(
	ctx context.Context,
	inst contracts.Instance,
	riskLevel contracts.RiskLevel,
	eventLevel contracts.EventLevel,
	result, title, text string,
) {
	if c.router == nil {
		return
	}
	routes, err := c.store.ListNotificationRoutes(ctx, inst.UserID)
	if err != nil {
		return
	}
	c.router.DispatchAll(ctx, notify.Event{
		UserID:     inst.UserID,
		InstanceID: inst.ID,
		EventLevel: eventLevel,
		RiskLevel:  riskLevel,
		Result:     result,
		Title:      title,
		Text:       text,
	}, routes)
}

func (c *Checker) storeSnapshot(s contracts.InstanceHealthSnapshot) {
	c.mu.Lock()
	c.snapshots[s.InstanceID] = s
	c.mu.Unlock()
}

func (c *Checker) getStreak(key string) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.failStreaks[key]
}

func (c *Checker) setStreak(key string, v int) {
	c.mu.Lock()
	c.failStreaks[key] = v
	c.mu.Unlock()
}

func (c *Checker) bumpStreak(key string) {
	c.mu.Lock()
	c.failStreaks[key]++
	c.mu.Unlock()
}

func (c *Checker) inCooldown(key string, cooldown time.Duration) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	last, ok := c.lastSwitched[key]
	if !ok {
		return false
	}
	if cooldown <= 0 {
		cooldown = c.cfg.Cooldown
	}
	return c.now().Sub(last) < cooldown
}

func (c *Checker) markSwitched(key string) {
	c.mu.Lock()
	c.lastSwitched[key] = c.now()
	c.mu.Unlock()
}

// accountHealthy treats "error" / "expired" / "disabled-by-upstream" statuses as
// unhealthy. Empty/active statuses are healthy.
func accountHealthy(ac contracts.GatewayAccount) bool {
	switch ac.Status {
	case "error", "expired", "rate_limited", "banned":
		return false
	default:
		return true
	}
}

// backupSignals gathers per-candidate runtime facts (currently the live fail
// streak) so the scorer can avoid switching into a flapping account. Reads the
// checker's streak map under its lock.
func (c *Checker) backupSignals(instanceID string, accounts []contracts.GatewayAccount) map[string]backupSignals {
	out := make(map[string]backupSignals, len(accounts))
	for _, ac := range accounts {
		out[ac.ID] = backupSignals{failStreak: c.getStreak(instanceID + "|" + ac.ID)}
	}
	return out
}

func accountLabel(ac contracts.GatewayAccount) string {
	if ac.DisplayName != "" {
		return ac.DisplayName
	}
	return ac.ID
}
