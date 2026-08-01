package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"e2m.local/contracts"
)

const notificationDeliveryColumns = `id,user_id,route_id,route_name,target_ref,template,channel,kind,status,
	event_level,risk_level,result,instance_id,title,text,fields,attempts,max_attempts,
	next_attempt_at,last_error_code,last_error_message,lease_owner,lease_until,lease_version,created_at,updated_at,sent_at,retried_from_id`

func scanNotificationDelivery(row rowScanner) (contracts.NotificationDelivery, error) {
	var d contracts.NotificationDelivery
	var channel, kind, status, eventLevel, riskLevel string
	var fields []byte
	var retriedFromID *string
	if err := row.Scan(
		&d.ID, &d.UserID, &d.RouteID, &d.RouteName, &d.TargetRef, &d.Template, &channel, &kind, &status,
		&eventLevel, &riskLevel, &d.Result, &d.InstanceID, &d.Title, &d.Text, &fields,
		&d.Attempts, &d.MaxAttempts, &d.NextAttemptAt, &d.LastErrorCode, &d.LastErrorMessage,
		&d.LeaseOwner, &d.LeaseUntil, &d.LeaseVersion, &d.CreatedAt, &d.UpdatedAt, &d.SentAt, &retriedFromID,
	); err != nil {
		return contracts.NotificationDelivery{}, mapNotFound(err)
	}
	d.Channel = contracts.NotificationChannel(channel)
	d.Kind = contracts.NotificationDeliveryKind(kind)
	d.Status = contracts.NotificationDeliveryStatus(status)
	d.EventLevel = contracts.EventLevel(eventLevel)
	d.RiskLevel = contracts.RiskLevel(riskLevel)
	if retriedFromID != nil {
		d.RetriedFromID = *retriedFromID
	}
	if len(fields) > 0 {
		_ = json.Unmarshal(fields, &d.Fields)
	}
	return d, nil
}

func (s *PostgresStore) CreateNotificationDelivery(ctx context.Context, input contracts.NotificationDelivery) (contracts.NotificationDelivery, error) {
	d, err := normalizeNotificationDelivery(input)
	if err != nil {
		return contracts.NotificationDelivery{}, err
	}
	if d.ID == "" {
		d.ID = newID("delivery")
	}
	now := time.Now().UTC()
	if input.NextAttemptAt.IsZero() {
		d.NextAttemptAt = now
	}
	d.CreatedAt, d.UpdatedAt = now, now
	fields, err := marshalLabels(d.Fields)
	if err != nil {
		return contracts.NotificationDelivery{}, err
	}
	row := s.pool.QueryRow(ctx, `INSERT INTO notification_deliveries (
		id,user_id,route_id,route_name,target_ref,template,channel,kind,status,event_level,risk_level,result,
		instance_id,title,text,fields,attempts,max_attempts,next_attempt_at,last_error_code,
		last_error_message,lease_owner,lease_until,lease_version,created_at,updated_at,sent_at,retried_from_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16::jsonb,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28)
		RETURNING `+notificationDeliveryColumns,
		d.ID, d.UserID, d.RouteID, d.RouteName, d.TargetRef, d.Template, string(d.Channel), string(d.Kind), string(d.Status),
		string(d.EventLevel), string(d.RiskLevel), d.Result, d.InstanceID, d.Title, d.Text, fields,
		d.Attempts, d.MaxAttempts, d.NextAttemptAt, d.LastErrorCode, d.LastErrorMessage,
		d.LeaseOwner, d.LeaseUntil, d.LeaseVersion, d.CreatedAt, d.UpdatedAt, d.SentAt, nullableString(d.RetriedFromID))
	return scanNotificationDelivery(row)
}

func (s *PostgresStore) GetNotificationDelivery(ctx context.Context, id string) (contracts.NotificationDelivery, error) {
	return scanNotificationDelivery(s.pool.QueryRow(ctx,
		`SELECT `+notificationDeliveryColumns+` FROM notification_deliveries WHERE id=$1`, id))
}

func (s *PostgresStore) ListNotificationDeliveries(ctx context.Context, filter contracts.NotificationDeliveryFilter) ([]contracts.NotificationDelivery, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `SELECT `+notificationDeliveryColumns+`
		FROM notification_deliveries
		WHERE ($1=0 OR user_id=$1) AND ($2='' OR route_id=$2) AND ($3='' OR status=$3)
		  AND ($4='' OR channel=$4) AND ($5='' OR target_ref=$5)
		ORDER BY created_at DESC LIMIT $6`, filter.UserID, strings.TrimSpace(filter.RouteID), string(filter.Status), string(filter.Channel), strings.TrimSpace(filter.TargetRef), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]contracts.NotificationDelivery, 0)
	for rows.Next() {
		d, err := scanNotificationDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ClaimNotificationDelivery(ctx context.Context, workerID string, leaseDuration time.Duration) (contracts.NotificationDelivery, bool, error) {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || leaseDuration <= 0 {
		return contracts.NotificationDelivery{}, false, ErrInvalid
	}
	leaseMicros := leaseDuration.Microseconds()
	row := s.pool.QueryRow(ctx, `WITH exhausted AS (
		UPDATE notification_deliveries
		   SET status='failed', last_error_code='lease_expired',
		       last_error_message='notification delivery lease expired after the final attempt',
		       lease_owner='', lease_until=NULL, updated_at=statement_timestamp()
		 WHERE status='processing' AND lease_until <= statement_timestamp() AND attempts >= max_attempts
		 RETURNING id
	), candidate AS (
		SELECT id FROM notification_deliveries
		 WHERE attempts < max_attempts
		   AND (((status IN ('pending','retrying')) AND next_attempt_at <= statement_timestamp())
		        OR (status='processing' AND lease_until <= statement_timestamp()))
		 ORDER BY next_attempt_at, created_at
		 FOR UPDATE SKIP LOCKED LIMIT 1
	)
	UPDATE notification_deliveries d
	   SET status='processing', attempts=d.attempts+1, lease_version=d.lease_version+1, lease_owner=$1,
	       lease_until=statement_timestamp()+($2 * interval '1 microsecond'), updated_at=statement_timestamp()
	  FROM candidate WHERE d.id=candidate.id
	RETURNING `+prefixedNotificationDeliveryColumns("d"), workerID, leaseMicros)
	d, err := scanNotificationDelivery(row)
	if err != nil {
		if err == ErrNotFound {
			return contracts.NotificationDelivery{}, false, nil
		}
		return contracts.NotificationDelivery{}, false, err
	}
	return d, true, nil
}

func prefixedNotificationDeliveryColumns(alias string) string {
	parts := strings.Split(notificationDeliveryColumns, ",")
	for i := range parts {
		parts[i] = alias + "." + strings.TrimSpace(parts[i])
	}
	return strings.Join(parts, ",")
}

func (s *PostgresStore) CompleteNotificationDelivery(ctx context.Context, id, workerID string, expectedLeaseVersion int64, succeeded bool, errorCode, errorMessage string, nextAttemptAt time.Time) (contracts.NotificationDelivery, error) {
	var next any = nil
	if !succeeded && !nextAttemptAt.IsZero() {
		next = nextAttemptAt.UTC()
	}
	row := s.pool.QueryRow(ctx, `UPDATE notification_deliveries
		SET status=CASE WHEN $4::boolean THEN 'succeeded'
		                WHEN attempts >= max_attempts OR $7::timestamptz IS NULL THEN 'failed'
		                ELSE 'retrying' END,
		    last_error_code=CASE WHEN $4 THEN '' ELSE $5 END,
		    last_error_message=CASE WHEN $4 THEN '' ELSE $6 END,
		    next_attempt_at=COALESCE($7,next_attempt_at), lease_owner='', lease_until=NULL,
		    sent_at=CASE WHEN $4 THEN statement_timestamp() ELSE sent_at END,
		    updated_at=statement_timestamp()
		WHERE id=$1 AND status='processing' AND lease_owner=$2 AND lease_version=$3
		  AND lease_until > statement_timestamp()
		RETURNING `+notificationDeliveryColumns, id, workerID, expectedLeaseVersion, succeeded, strings.TrimSpace(errorCode), strings.TrimSpace(errorMessage), next)
	d, err := scanNotificationDelivery(row)
	if err != nil {
		if err == ErrNotFound {
			if _, getErr := s.GetNotificationDelivery(ctx, id); getErr == nil {
				return contracts.NotificationDelivery{}, ErrConflict
			}
		}
		return contracts.NotificationDelivery{}, err
	}
	return d, nil
}

func (s *PostgresStore) RetryNotificationDelivery(ctx context.Context, id string) (contracts.NotificationDelivery, error) {
	newID := newID("delivery")
	d, err := scanNotificationDelivery(s.pool.QueryRow(ctx, `INSERT INTO notification_deliveries (
		id,user_id,route_id,route_name,target_ref,template,channel,kind,status,event_level,risk_level,result,
		instance_id,title,text,fields,attempts,max_attempts,next_attempt_at,last_error_code,
		last_error_message,lease_owner,lease_until,lease_version,sent_at,retried_from_id,created_at,updated_at)
		SELECT $2,user_id,route_id,route_name,target_ref,template,channel,kind,'pending',event_level,risk_level,result,
		       instance_id,title,text,fields,0,max_attempts,statement_timestamp(),'','', '',NULL,0,NULL,id,
		       statement_timestamp(),statement_timestamp()
		  FROM notification_deliveries WHERE id=$1 AND status='failed'
		RETURNING `+notificationDeliveryColumns, id, newID))
	if isUniqueViolation(err) {
		return contracts.NotificationDelivery{}, ErrConflict
	}
	if err == ErrNotFound {
		if _, getErr := s.GetNotificationDelivery(ctx, id); getErr == nil {
			return contracts.NotificationDelivery{}, ErrConflict
		}
	}
	return d, err
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.TrimSpace(value)
}
