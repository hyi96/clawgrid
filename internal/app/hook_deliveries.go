package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	AccountHookStatusPending = "pending_verification"
	AccountHookStatusActive  = "active"

	HookNotificationAssignmentReceived = "assignment_received"
	HookNotificationReplyReceived      = "reply_received"

	HookDeliveryStatusPending   = "pending"
	HookDeliveryStatusSending   = "sending"
	HookDeliveryStatusDelivered = "delivered"
	HookDeliveryStatusFailed    = "failed"
	HookDeliveryStatusSkipped   = "skipped"

	HookAutoDisableFailureLimit = 5
	hookDeliveryBatchSize       = 20
	hookDeliveryStaleWindow     = time.Minute
)

type HookDelivery struct {
	URL       string
	AuthToken string
	Message   string
	Name      string
}

type hookDeliveryPayload struct {
	Message string `json:"message"`
	Name    string `json:"name"`
}

type pendingHookDelivery struct {
	ID        string
	AccountID string
	Kind      string
	Message   string
	Name      string
}

type hookTarget struct {
	URL                      string
	AuthToken                string
	Enabled                  bool
	Status                   string
	NotifyAssignmentReceived bool
	NotifyReplyReceived      bool
}

func (s *Service) SetHookDeliveryFunc(fn func(context.Context, HookDelivery) error) {
	if fn == nil {
		s.deliverHook = s.deliverHookRequest
		return
	}
	s.deliverHook = fn
}

func (s *Service) deliverHookRequest(ctx context.Context, delivery HookDelivery) error {
	payload := hookDeliveryPayload{
		Message: delivery.Message,
		Name:    delivery.Name,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return errors.New("hook_delivery_failed")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, delivery.URL, bytes.NewReader(body))
	if err != nil {
		return errors.New("hook_delivery_failed")
	}
	req.Header.Set("Content-Type", "application/json")
	if delivery.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+delivery.AuthToken)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return errors.New("hook_delivery_failed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return errors.New("hook_delivery_failed")
	}
	return nil
}

func hookAllowsNotification(target hookTarget, kind string) bool {
	if !target.Enabled || target.Status != AccountHookStatusActive {
		return false
	}
	switch kind {
	case HookNotificationAssignmentReceived:
		return target.NotifyAssignmentReceived
	case HookNotificationReplyReceived:
		return target.NotifyReplyReceived
	default:
		return false
	}
}

func (s *Service) failStaleHookDeliveries(ctx context.Context) error {
	_, err := s.DB.Exec(ctx, `
UPDATE account_hook_deliveries
SET status = $1,
    failure_reason = $2
WHERE status = $3
  AND attempted_at <= now() - make_interval(secs => $4::int)`,
		HookDeliveryStatusFailed,
		"delivery_abandoned",
		HookDeliveryStatusSending,
		int(hookDeliveryStaleWindow.Seconds()),
	)
	return err
}

func (s *Service) claimPendingHookDeliveries(ctx context.Context) ([]pendingHookDelivery, error) {
	if err := s.failStaleHookDeliveries(ctx); err != nil {
		return nil, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
WITH candidates AS (
  SELECT id
  FROM account_hook_deliveries
  WHERE status = $1
  ORDER BY created_at ASC, id ASC
  LIMIT $2
  FOR UPDATE SKIP LOCKED
)
UPDATE account_hook_deliveries d
SET status = $3,
    attempted_at = now()
FROM candidates c
WHERE d.id = c.id
RETURNING d.id, d.account_id, d.kind, d.message, d.name`,
		HookDeliveryStatusPending,
		hookDeliveryBatchSize,
		HookDeliveryStatusSending,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]pendingHookDelivery, 0)
	for rows.Next() {
		var item pendingHookDelivery
		if err := rows.Scan(&item.ID, &item.AccountID, &item.Kind, &item.Message, &item.Name); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Service) loadHookTarget(ctx context.Context, accountID string) (*hookTarget, error) {
	var target hookTarget
	err := s.DB.QueryRow(ctx, `
SELECT url, auth_token, enabled, status, notify_assignment_received, notify_reply_received
FROM account_hooks
WHERE account_id = $1`, accountID).Scan(
		&target.URL,
		&target.AuthToken,
		&target.Enabled,
		&target.Status,
		&target.NotifyAssignmentReceived,
		&target.NotifyReplyReceived,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &target, nil
}

func (s *Service) markHookDeliverySkipped(ctx context.Context, deliveryID, reason string) error {
	_, err := s.DB.Exec(ctx, `
UPDATE account_hook_deliveries
SET status = $2,
    failure_reason = $3
WHERE id = $1`, deliveryID, HookDeliveryStatusSkipped, reason)
	return err
}

func (s *Service) markHookDeliverySuccess(ctx context.Context, deliveryID, accountID string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
UPDATE account_hook_deliveries
SET status = $2,
    delivered_at = now(),
    failure_reason = ''
WHERE id = $1`, deliveryID, HookDeliveryStatusDelivered); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
UPDATE account_hooks
SET last_success_at = now(),
    last_failure_at = NULL,
    consecutive_failures = 0,
    failure_reason = '',
    updated_at = now()
WHERE account_id = $1`, accountID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) markHookDeliveryFailure(ctx context.Context, delivery pendingHookDelivery, failureReason string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
UPDATE account_hooks
SET last_failure_at = now(),
    consecutive_failures = CASE
      WHEN consecutive_failures + 1 >= $3 THEN 0
      ELSE consecutive_failures + 1
    END,
    enabled = CASE
      WHEN consecutive_failures + 1 >= $3 THEN FALSE
      ELSE enabled
    END,
    status = CASE
      WHEN consecutive_failures + 1 >= $3 THEN $4
      ELSE status
    END,
    verification_token = CASE
      WHEN consecutive_failures + 1 >= $3 THEN NULL
      ELSE verification_token
    END,
    verified_at = CASE
      WHEN consecutive_failures + 1 >= $3 THEN NULL
      ELSE verified_at
    END,
    failure_reason = $2,
    updated_at = now()
WHERE account_id = $1`, delivery.AccountID, failureReason, HookAutoDisableFailureLimit, AccountHookStatusPending); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
UPDATE account_hook_deliveries
SET status = $2,
    failure_reason = $3
WHERE id = $1`, delivery.ID, HookDeliveryStatusFailed, failureReason); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) ProcessAccountHookDeliveries(ctx context.Context) (int64, error) {
	items, err := s.claimPendingHookDeliveries(ctx)
	if err != nil {
		return 0, err
	}
	var affected int64
	for _, item := range items {
		target, err := s.loadHookTarget(ctx, item.AccountID)
		if err != nil {
			return affected, err
		}
		if target == nil {
			if err := s.markHookDeliverySkipped(ctx, item.ID, "hook_not_found"); err != nil {
				return affected, err
			}
			affected++
			continue
		}
		if !hookAllowsNotification(*target, item.Kind) {
			if err := s.markHookDeliverySkipped(ctx, item.ID, "hook_not_active"); err != nil {
				return affected, err
			}
			affected++
			continue
		}
		if err := s.deliverHook(ctx, HookDelivery{
			URL:       target.URL,
			AuthToken: target.AuthToken,
			Message:   item.Message,
			Name:      item.Name,
		}); err != nil {
			if markErr := s.markHookDeliveryFailure(ctx, item, err.Error()); markErr != nil {
				return affected, markErr
			}
			affected++
			continue
		}
		if err := s.markHookDeliverySuccess(ctx, item.ID, item.AccountID); err != nil {
			return affected, err
		}
		affected++
	}
	return affected, nil
}
