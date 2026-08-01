// Package approval implements the L2/L3 human-decision gate (W6). High-risk
// actions are submitted as ApprovalRequests; nothing executes until an operator
// approves. Every transition is audited with the ApprovalID, and pending
// requests are announced over the notify router (Feishu card-style text).
package approval

import (
	"context"
	"fmt"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/notify"
	"e2m.local/core/internal/orchestrator"
	"e2m.local/core/internal/store"
)

// Engine owns the approval lifecycle: submit -> approve/reject -> execute.
type Engine struct {
	store  store.Store
	orch   *orchestrator.Orchestrator
	router *notify.Router
	now    func() time.Time
}

func New(st store.Store, orch *orchestrator.Orchestrator, router *notify.Router) *Engine {
	return &Engine{store: st, orch: orch, router: router, now: time.Now}
}

// Submit validates and stores a new approval request, audits it, and notifies.
func (e *Engine) Submit(ctx context.Context, req contracts.ApprovalRequest) (contracts.ApprovalRequest, error) {
	if req.Action != contracts.ApprovalActionBatchSchedulable {
		return contracts.ApprovalRequest{}, fmt.Errorf("approval: unsupported action %q", req.Action)
	}
	if len(req.AccountIDs) == 0 {
		return contracts.ApprovalRequest{}, fmt.Errorf("approval: account_ids is required")
	}
	if req.Schedulable == nil {
		return contracts.ApprovalRequest{}, fmt.Errorf("approval: schedulable is required")
	}
	inst, err := e.store.GetInstance(ctx, req.InstanceID)
	if err != nil {
		return contracts.ApprovalRequest{}, err
	}
	// An approval is an unfenced manual operation. Reject managed RoutePlan
	// accounts before creating a request; their scheduling is owned by publish
	// and auto-switch. The same validation is repeated after approval because an
	// account can become managed while the request is pending.
	if err := e.orch.ValidateSchedulableAccounts(ctx, req.InstanceID, req.AccountIDs); err != nil {
		return contracts.ApprovalRequest{}, fmt.Errorf("approval: schedulable preflight: %w", err)
	}

	req.UserID = inst.UserID
	req.RiskLevel = contracts.RiskLevelL2
	req.Status = contracts.ApprovalPending
	if req.RequestedBy == "" {
		req.RequestedBy = "console"
	}

	ap, err := e.store.CreateApproval(ctx, req)
	if err != nil {
		return contracts.ApprovalRequest{}, err
	}

	e.audit(ctx, ap, "approval.submit", "accepted", "")
	e.notify(ctx, inst, ap, contracts.EventLevelNotice, "pending", "🟠 待审批 · "+inst.Name,
		fmt.Sprintf("动作：批量%s %d 个账号\n原因：%s\n审批单：%s\n请在控制台【审批中心】处理",
			verb(*ap.Schedulable), len(ap.AccountIDs), orDash(ap.Reason), ap.ID))
	return ap, nil
}

// Approve executes the gated action and moves the request to executed/failed.
func (e *Engine) Approve(ctx context.Context, id, decidedBy string) (contracts.ApprovalRequest, error) {
	ap, err := e.store.GetApproval(ctx, id)
	if err != nil {
		return contracts.ApprovalRequest{}, err
	}
	if ap.Status != contracts.ApprovalPending {
		return contracts.ApprovalRequest{}, fmt.Errorf("approval: request is %s, not pending", ap.Status)
	}

	now := e.now().UTC()
	ap.DecidedBy = decidedBy
	ap.DecidedAt = &now
	ap.Status = contracts.ApprovalApproved
	ap, err = e.store.TransitionApproval(ctx, ap, contracts.ApprovalPending)
	if err != nil {
		return contracts.ApprovalRequest{}, err
	}
	// The pending -> approved CAS owns all execution side effects. Finish the
	// claimed request even when the initiating HTTP request is canceled.
	runCtx := context.WithoutCancel(ctx)
	e.audit(runCtx, ap, "approval.approve", "accepted", "")

	// Execute: batch SetSchedulable through the orchestrator (each call audited
	// per-account by the orchestrator; approval-level audit carries ApprovalID).
	var failures []string
	if err := e.orch.ValidateSchedulableAccounts(runCtx, ap.InstanceID, ap.AccountIDs); err != nil {
		failures = append(failures, "batch preflight: "+err.Error())
	} else {
		for _, accID := range ap.AccountIDs {
			reason := fmt.Sprintf("审批 %s：%s", ap.ID, orDash(ap.Reason))
			if err := e.orch.SetSchedulable(runCtx, ap.InstanceID, accID, *ap.Schedulable, reason); err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", accID, err))
			}
		}
	}

	inst, _ := e.store.GetInstance(runCtx, ap.InstanceID)
	if len(failures) == 0 {
		ap.Status = contracts.ApprovalExecuted
		ap.ResultNote = fmt.Sprintf("已批量%s %d 个账号", verb(*ap.Schedulable), len(ap.AccountIDs))
	} else {
		ap.Status = contracts.ApprovalFailed
		ap.ResultNote = "部分或全部执行失败：" + strings.Join(failures, "; ")
	}
	ap, err = e.store.TransitionApproval(runCtx, ap, contracts.ApprovalApproved)
	if err != nil {
		return contracts.ApprovalRequest{}, err
	}
	if len(failures) == 0 {
		e.audit(runCtx, ap, "approval.execute", "accepted", "")
		e.notify(runCtx, inst, ap, contracts.EventLevelNotice, "accepted", "✅ 审批已执行 · "+inst.Name, ap.ResultNote+"\n审批人："+decidedBy)
	} else {
		e.audit(runCtx, ap, "approval.execute", "failed", ap.ResultNote)
		e.notify(runCtx, inst, ap, contracts.EventLevelWarning, "failed", "❌ 审批执行失败 · "+inst.Name, ap.ResultNote)
	}
	return ap, nil
}

// Reject closes the request without executing.
func (e *Engine) Reject(ctx context.Context, id, decidedBy, note string) (contracts.ApprovalRequest, error) {
	ap, err := e.store.GetApproval(ctx, id)
	if err != nil {
		return contracts.ApprovalRequest{}, err
	}
	if ap.Status != contracts.ApprovalPending {
		return contracts.ApprovalRequest{}, fmt.Errorf("approval: request is %s, not pending", ap.Status)
	}
	now := e.now().UTC()
	ap.Status = contracts.ApprovalRejected
	ap.DecidedBy = decidedBy
	ap.DecidedAt = &now
	ap.ResultNote = note
	ap, err = e.store.TransitionApproval(ctx, ap, contracts.ApprovalPending)
	if err != nil {
		return contracts.ApprovalRequest{}, err
	}
	e.audit(ctx, ap, "approval.reject", "accepted", "")
	inst, _ := e.store.GetInstance(ctx, ap.InstanceID)
	e.notify(ctx, inst, ap, contracts.EventLevelNotice, "rejected", "⛔ 审批已驳回 · "+inst.Name,
		fmt.Sprintf("审批单 %s 被 %s 驳回\n备注：%s", ap.ID, decidedBy, orDash(note)))
	return ap, nil
}

func (e *Engine) audit(ctx context.Context, ap contracts.ApprovalRequest, action, result, errMsg string) {
	actorType, actorID := "operator", firstNonEmpty(ap.DecidedBy, ap.RequestedBy, "console")
	if a, ok := contracts.ActorFromContext(ctx); ok {
		actorType, actorID = a.Type, a.ID
	}
	_, _ = e.store.AppendAudit(ctx, contracts.OperationAudit{
		UserID:       ap.UserID,
		InstanceID:   ap.InstanceID,
		ActorType:    actorType,
		ActorID:      actorID,
		Action:       action,
		RiskLevel:    ap.RiskLevel,
		TargetType:   "approval",
		TargetID:     ap.ID,
		Result:       result,
		ErrorMessage: errMsg,
		ApprovalID:   ap.ID,
	})
}

func (e *Engine) notify(
	ctx context.Context,
	inst contracts.Instance,
	ap contracts.ApprovalRequest,
	eventLevel contracts.EventLevel,
	result, title, text string,
) {
	if e.router == nil {
		return
	}
	routes, err := e.store.ListNotificationRoutes(ctx, ap.UserID)
	if err != nil {
		return
	}
	e.router.DispatchAll(ctx, notify.Event{
		UserID:     ap.UserID,
		InstanceID: ap.InstanceID,
		EventLevel: eventLevel,
		RiskLevel:  ap.RiskLevel,
		Result:     result,
		Title:      title,
		Text:       text,
	}, routes)
}

func verb(schedulable bool) string {
	if schedulable {
		return "启用"
	}
	return "停用"
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
