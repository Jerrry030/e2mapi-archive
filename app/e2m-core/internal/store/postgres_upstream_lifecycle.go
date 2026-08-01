package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"e2m.local/contracts"
)

func isCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514"
}

func (s *PostgresStore) SetUpstreamPoolSafetyStock(ctx context.Context, poolID string, threshold int) error {
	if threshold < 0 {
		return ErrInvalid
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE upstream_pools SET safety_stock_threshold=$2, updated_at=statement_timestamp() WHERE id=$1`,
		strings.TrimSpace(poolID), threshold)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) SetUpstreamInventoryState(ctx context.Context, channelID string, state contracts.UpstreamInventoryState) (contracts.UpstreamInventoryStateRecord, error) {
	if !state.Valid() {
		return contracts.UpstreamInventoryStateRecord{}, ErrInvalid
	}
	var out contracts.UpstreamInventoryStateRecord
	err := s.pool.QueryRow(ctx,
		`UPDATE upstream_channels SET inventory_state=$2, updated_at=statement_timestamp()
		  WHERE id=$1 RETURNING id, inventory_state, updated_at`, strings.TrimSpace(channelID), string(state),
	).Scan(&out.ChannelID, &out.State, &out.UpdatedAt)
	if err != nil {
		if isCheckViolation(err) {
			return contracts.UpstreamInventoryStateRecord{}, ErrConflict
		}
		return contracts.UpstreamInventoryStateRecord{}, mapNotFound(err)
	}
	return out, nil
}

func (s *PostgresStore) ImportUpstreamInventory(ctx context.Context, poolID string, entries []contracts.UpstreamInventoryImportEntry) (contracts.UpstreamInventoryImportResult, error) {
	poolID = strings.TrimSpace(poolID)
	if poolID == "" || len(entries) == 0 {
		return contracts.UpstreamInventoryImportResult{}, ErrInvalid
	}
	for _, entry := range entries {
		channel := entry.Channel
		if channel.PoolID != "" && channel.PoolID != poolID || !contracts.IsUpstreamSourceIdentity(channel.SourceID) ||
			strings.TrimSpace(channel.DisplayName) == "" || channel.AccountOwnership.Normalize() != contracts.GatewayAccountPlatformManaged ||
			strings.TrimSpace(entry.SecretRef) == "" || strings.TrimSpace(entry.MaskedValue) == "" {
			return contracts.UpstreamInventoryImportResult{}, ErrInvalid
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.UpstreamInventoryImportResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var poolExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM upstream_pools WHERE id=$1)`, poolID).Scan(&poolExists); err != nil {
		return contracts.UpstreamInventoryImportResult{}, err
	}
	if !poolExists {
		return contracts.UpstreamInventoryImportResult{}, ErrNotFound
	}
	result := contracts.UpstreamInventoryImportResult{Channels: make([]contracts.UpstreamInventoryImportedChannel, 0, len(entries))}
	for _, entry := range entries {
		channel := entry.Channel
		channel.PoolID = poolID
		channel.AccountOwnership = contracts.GatewayAccountPlatformManaged
		channel.Status = contracts.UpstreamChannelMaintenance
		channel.InventoryState = contracts.UpstreamInventoryDraft
		if channel.ID == "" {
			channel.ID = newID("uchan")
		}
		labels, marshalErr := marshalLabels(channel.Labels)
		if marshalErr != nil {
			return contracts.UpstreamInventoryImportResult{}, marshalErr
		}
		err := tx.QueryRow(ctx,
			`INSERT INTO upstream_channels
			 (id,pool_id,source_id,account_ownership,display_name,provider,models,probe_capability,probe_endpoint_path,
			  groups,credential_binding_id,proxy_binding_id,priority,weight,cost_hint,status,inventory_state,labels,created_at,updated_at)
			 VALUES ($1,$2,$3,'platform_managed',$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'maintenance','draft',$15,
			         statement_timestamp(),statement_timestamp())
			 RETURNING created_at,updated_at`,
			channel.ID, channel.PoolID, strings.TrimSpace(channel.SourceID), channel.DisplayName, channel.Provider,
			marshalStrings(channel.Models), string(channel.ProbeCapability), channel.ProbeEndpointPath, marshalStrings(channel.Groups),
			channel.CredentialBindingID, channel.ProxyBindingID, channel.Priority, channel.Weight, channel.CostHint, labels,
		).Scan(&channel.CreatedAt, &channel.UpdatedAt)
		if err != nil {
			if isUniqueViolation(err) {
				return contracts.UpstreamInventoryImportResult{}, ErrDuplicate
			}
			return contracts.UpstreamInventoryImportResult{}, err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO upstream_key_deliveries
			 (id,channel_id,secret_ref,masked_value,key_version,proof_status,proof_connector_id,created_at,updated_at)
			 VALUES ($1,$2,$3,$4,1,'unverified','',statement_timestamp(),statement_timestamp())`,
			newID("keydel"), channel.ID, entry.SecretRef, entry.MaskedValue); err != nil {
			if isUniqueViolation(err) {
				return contracts.UpstreamInventoryImportResult{}, ErrDuplicate
			}
			return contracts.UpstreamInventoryImportResult{}, err
		}
		result.Channels = append(result.Channels, contracts.SafeImportedUpstreamChannel(channel))
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.UpstreamInventoryImportResult{}, err
	}
	result.Imported = len(result.Channels)
	return result, nil
}

func (s *PostgresStore) MigrateUpstreamChannel(ctx context.Context, channelID, targetPoolID, reason string, actorUserID int64) (contracts.UpstreamChannelMigration, error) {
	channelID, targetPoolID, reason = strings.TrimSpace(channelID), strings.TrimSpace(targetPoolID), strings.TrimSpace(reason)
	if channelID == "" || targetPoolID == "" || reason == "" {
		return contracts.UpstreamChannelMigration{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.UpstreamChannelMigration{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var fromPoolID string
	if err := tx.QueryRow(ctx, `SELECT pool_id FROM upstream_channels WHERE id=$1 FOR UPDATE`, channelID).Scan(&fromPoolID); err != nil {
		return contracts.UpstreamChannelMigration{}, mapNotFound(err)
	}
	if fromPoolID == targetPoolID {
		return contracts.UpstreamChannelMigration{}, ErrConflict
	}
	var targetExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM upstream_pools WHERE id=$1)`, targetPoolID).Scan(&targetExists); err != nil {
		return contracts.UpstreamChannelMigration{}, err
	}
	if !targetExists {
		return contracts.UpstreamChannelMigration{}, ErrNotFound
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('e2m.channel_pool_migration','allowed',true)`); err != nil {
		return contracts.UpstreamChannelMigration{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE upstream_channels SET pool_id=$2,updated_at=statement_timestamp() WHERE id=$1`, channelID, targetPoolID); err != nil {
		return contracts.UpstreamChannelMigration{}, err
	}
	var migration contracts.UpstreamChannelMigration
	err = tx.QueryRow(ctx,
		`INSERT INTO upstream_channel_migrations (id,channel_id,from_pool_id,to_pool_id,reason,actor_user_id,migrated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,statement_timestamp())
		 RETURNING channel_id,from_pool_id,to_pool_id,reason,migrated_at`,
		newID("uchmig"), channelID, fromPoolID, targetPoolID, reason, actorUserID,
	).Scan(&migration.ChannelID, &migration.FromPoolID, &migration.ToPoolID, &migration.Reason, &migration.MigratedAt)
	if err != nil {
		return contracts.UpstreamChannelMigration{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.UpstreamChannelMigration{}, err
	}
	return migration, nil
}

type postgresRotationRecord struct {
	delivery        contracts.UpstreamKeyDelivery
	previousRef     string
	previousMask    string
	previousVersion int64
	status          contracts.KeyRotationStatus
	startedAt       *time.Time
}

func scanPostgresRotation(row rowScanner) (postgresRotationRecord, error) {
	var record postgresRotationRecord
	err := row.Scan(
		&record.delivery.ID, &record.delivery.ChannelID, &record.delivery.SecretRef, &record.delivery.MaskedValue,
		&record.delivery.KeyVersion, &record.delivery.ProofStatus, &record.delivery.ProofConnectorID,
		&record.delivery.ProofCheckedAt, &record.delivery.CreatedAt, &record.delivery.UpdatedAt,
		&record.previousRef, &record.previousMask, &record.previousVersion, &record.status, &record.startedAt,
	)
	return record, err
}

const postgresRotationColumns = `id,channel_id,secret_ref,masked_value,key_version,proof_status,
	proof_connector_id,proof_checked_at,created_at,updated_at,
	COALESCE(previous_secret_ref,''),previous_masked_value,previous_key_version,rotation_status,rotation_started_at`

func (s *PostgresStore) StartUpstreamKeyRotation(ctx context.Context, channelID, secretRef, maskedValue string) (contracts.UpstreamKeyRotation, error) {
	channelID, secretRef, maskedValue = strings.TrimSpace(channelID), strings.TrimSpace(secretRef), strings.TrimSpace(maskedValue)
	if channelID == "" || secretRef == "" || maskedValue == "" {
		return contracts.UpstreamKeyRotation{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.UpstreamKeyRotation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	record, err := scanPostgresRotation(tx.QueryRow(ctx, `SELECT `+postgresRotationColumns+` FROM upstream_key_deliveries WHERE channel_id=$1 FOR UPDATE`, channelID))
	if errors.Is(err, pgx.ErrNoRows) {
		var ownership string
		if err := tx.QueryRow(ctx, `SELECT account_ownership FROM upstream_channels WHERE id=$1`, channelID).Scan(&ownership); err != nil {
			return contracts.UpstreamKeyRotation{}, mapNotFound(err)
		}
		if ownership != string(contracts.GatewayAccountPlatformManaged) {
			return contracts.UpstreamKeyRotation{}, ErrConflict
		}
		err = tx.QueryRow(ctx,
			`INSERT INTO upstream_key_deliveries
			 (id,channel_id,secret_ref,masked_value,key_version,proof_status,proof_connector_id,rotation_status,created_at,updated_at)
			 VALUES ($1,$2,$3,$4,1,'unverified','','stable',statement_timestamp(),statement_timestamp())
			 RETURNING `+postgresRotationColumns,
			newID("keydel"), channelID, secretRef, maskedValue,
		).Scan(
			&record.delivery.ID, &record.delivery.ChannelID, &record.delivery.SecretRef, &record.delivery.MaskedValue,
			&record.delivery.KeyVersion, &record.delivery.ProofStatus, &record.delivery.ProofConnectorID,
			&record.delivery.ProofCheckedAt, &record.delivery.CreatedAt, &record.delivery.UpdatedAt,
			&record.previousRef, &record.previousMask, &record.previousVersion, &record.status, &record.startedAt,
		)
		if err != nil {
			return contracts.UpstreamKeyRotation{}, err
		}
	} else if err != nil {
		return contracts.UpstreamKeyRotation{}, err
	} else {
		if record.status != contracts.KeyRotationStable {
			return contracts.UpstreamKeyRotation{}, ErrConflict
		}
		record, err = scanPostgresRotation(tx.QueryRow(ctx,
			`UPDATE upstream_key_deliveries SET
			 previous_secret_ref=secret_ref,previous_masked_value=masked_value,previous_key_version=key_version,
			 secret_ref=$2,masked_value=$3,key_version=key_version+1,proof_status='unverified',proof_connector_id='',proof_checked_at=NULL,
			 rotation_status='deploying',rotation_resume_status='',rotation_started_at=statement_timestamp(),updated_at=statement_timestamp()
			 WHERE channel_id=$1 AND rotation_status='stable' RETURNING `+postgresRotationColumns,
			channelID, secretRef, maskedValue))
		if err != nil {
			return contracts.UpstreamKeyRotation{}, mapNotFound(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.UpstreamKeyRotation{}, err
	}
	return s.postgresRotationView(ctx, record)
}

func (s *PostgresStore) GetUpstreamKeyRotation(ctx context.Context, channelID string) (contracts.UpstreamKeyRotation, error) {
	record, err := scanPostgresRotation(s.pool.QueryRow(ctx, `SELECT `+postgresRotationColumns+` FROM upstream_key_deliveries WHERE channel_id=$1`, strings.TrimSpace(channelID)))
	if err != nil {
		return contracts.UpstreamKeyRotation{}, mapNotFound(err)
	}
	return s.postgresRotationView(ctx, record)
}

func (s *PostgresStore) postgresRotationView(ctx context.Context, record postgresRotationRecord) (contracts.UpstreamKeyRotation, error) {
	view := contracts.UpstreamKeyRotation{
		ChannelID: record.delivery.ChannelID, CurrentKeyVersion: record.delivery.KeyVersion,
		CurrentMaskedValue: record.delivery.MaskedValue, PreviousKeyVersion: record.previousVersion,
		PreviousMaskedValue: record.previousMask, Status: record.status, StartedAt: record.startedAt,
		CanRollback: record.previousRef != "" && (record.status == contracts.KeyRotationDeploying || record.status == contracts.KeyRotationRollingBack),
		UpdatedAt:   record.delivery.UpdatedAt,
	}
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT COALESCE(NULLIF(BTRIM(pb.instance_id),''),rp.instance_id)
		   FROM published_bindings pb JOIN route_plans rp ON rp.id=pb.plan_id
		  WHERE pb.channel_id=$1 AND pb.state<>'revoked'`, record.delivery.ChannelID)
	if err != nil {
		return contracts.UpstreamKeyRotation{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var instanceID string
		if err := rows.Scan(&instanceID); err != nil {
			return contracts.UpstreamKeyRotation{}, err
		}
		if strings.TrimSpace(instanceID) != "" {
			view.PendingInstances = append(view.PendingInstances, instanceID)
		}
	}
	if err := rows.Err(); err != nil {
		return contracts.UpstreamKeyRotation{}, err
	}
	view.TargetInstances = len(view.PendingInstances)
	if view.TargetInstances > 0 {
		confirmedRows, err := s.pool.Query(ctx,
			`SELECT DISTINCT p.instance_id
			   FROM upstream_key_proof_receipts p
			   JOIN upstream_key_deployments d ON d.channel_id=p.channel_id AND d.instance_id=p.instance_id
			  WHERE p.channel_id=$1 AND p.key_version=$2 AND p.status='verified'
			    AND d.key_version=$2 AND d.status='deployed'`, record.delivery.ChannelID, record.delivery.KeyVersion)
		if err != nil {
			return contracts.UpstreamKeyRotation{}, err
		}
		confirmed := make(map[string]struct{})
		for confirmedRows.Next() {
			var instanceID string
			if err := confirmedRows.Scan(&instanceID); err != nil {
				confirmedRows.Close()
				return contracts.UpstreamKeyRotation{}, err
			}
			confirmed[instanceID] = struct{}{}
		}
		confirmedRows.Close()
		pending := view.PendingInstances[:0]
		for _, instanceID := range view.PendingInstances {
			if _, ok := confirmed[instanceID]; ok {
				view.ConfirmedInstances++
			} else {
				pending = append(pending, instanceID)
			}
		}
		view.PendingInstances = pending
	}
	view.CanFinalize = record.previousRef != "" && view.ConfirmedInstances == view.TargetInstances &&
		(record.status == contracts.KeyRotationDeploying || record.status == contracts.KeyRotationRollingBack || record.status == contracts.KeyRotationFinalizing)
	return view, nil
}

func (s *PostgresStore) BeginUpstreamKeyRotationRollback(ctx context.Context, channelID string) (contracts.KeyRotationSecrets, error) {
	record, err := scanPostgresRotation(s.pool.QueryRow(ctx,
		`UPDATE upstream_key_deliveries SET
		 secret_ref=previous_secret_ref,masked_value=previous_masked_value,key_version=key_version+1,
		 previous_secret_ref=secret_ref,previous_masked_value=masked_value,previous_key_version=key_version,
		 proof_status='unverified',proof_connector_id='',proof_checked_at=NULL,
		 rotation_status='rolling_back',rotation_resume_status='',rotation_started_at=statement_timestamp(),updated_at=statement_timestamp()
		 WHERE channel_id=$1 AND previous_secret_ref IS NOT NULL AND rotation_status IN ('deploying','rolling_back')
		 RETURNING `+postgresRotationColumns, strings.TrimSpace(channelID)))
	if err != nil {
		return contracts.KeyRotationSecrets{}, mapConflictOrNotFound(ctx, s, channelID, err)
	}
	view, err := s.postgresRotationView(ctx, record)
	return contracts.KeyRotationSecrets{Rotation: view, CurrentSecretRef: record.delivery.SecretRef, PreviousSecretRef: record.previousRef}, err
}

func (s *PostgresStore) BeginUpstreamKeyRotationFinalize(ctx context.Context, channelID string) (contracts.KeyRotationSecrets, error) {
	view, err := s.GetUpstreamKeyRotation(ctx, channelID)
	if err != nil {
		return contracts.KeyRotationSecrets{}, err
	}
	if !view.CanFinalize {
		return contracts.KeyRotationSecrets{}, ErrConflict
	}
	var secrets contracts.KeyRotationSecrets
	err = s.pool.QueryRow(ctx,
		`UPDATE upstream_key_deliveries SET
		 rotation_resume_status=CASE WHEN rotation_status='finalizing' THEN rotation_resume_status ELSE rotation_status END,
		 rotation_status='finalizing',updated_at=statement_timestamp()
		 WHERE channel_id=$1 AND key_version=$2 AND previous_secret_ref IS NOT NULL
		   AND rotation_status IN ('deploying','rolling_back','finalizing')
		   AND NOT EXISTS (
		     SELECT 1
		       FROM published_bindings pb
		       JOIN route_plans rp ON rp.id=pb.plan_id
		       LEFT JOIN upstream_key_proof_receipts p
		         ON p.channel_id=pb.channel_id
		        AND p.instance_id=COALESCE(NULLIF(BTRIM(pb.instance_id),''),rp.instance_id)
		       LEFT JOIN upstream_key_deployments d
		         ON d.channel_id=pb.channel_id
		        AND d.instance_id=COALESCE(NULLIF(BTRIM(pb.instance_id),''),rp.instance_id)
		      WHERE pb.channel_id=$1 AND pb.state<>'revoked'
		        AND (p.key_version IS DISTINCT FROM $2 OR p.status IS DISTINCT FROM 'verified'
		             OR d.key_version IS DISTINCT FROM $2 OR d.status IS DISTINCT FROM 'deployed')
		   )
		 RETURNING secret_ref,previous_secret_ref`, channelID, view.CurrentKeyVersion,
	).Scan(&secrets.CurrentSecretRef, &secrets.PreviousSecretRef)
	if err != nil {
		return contracts.KeyRotationSecrets{}, mapNotFound(err)
	}
	view.Status = contracts.KeyRotationFinalizing
	secrets.Rotation = view
	return secrets, nil
}

func (s *PostgresStore) CompleteUpstreamKeyRotationFinalize(ctx context.Context, channelID string, expectedVersion int64) (contracts.UpstreamKeyRotation, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE upstream_key_deliveries SET previous_secret_ref=NULL,previous_masked_value='',previous_key_version=0,
		 rotation_status='stable',rotation_resume_status='',rotation_started_at=NULL,updated_at=statement_timestamp()
		 WHERE channel_id=$1 AND key_version=$2 AND rotation_status='finalizing'`, channelID, expectedVersion)
	if err != nil {
		return contracts.UpstreamKeyRotation{}, err
	}
	if tag.RowsAffected() == 0 {
		return contracts.UpstreamKeyRotation{}, ErrConflict
	}
	return s.GetUpstreamKeyRotation(ctx, channelID)
}

func (s *PostgresStore) AbortUpstreamKeyRotationFinalize(ctx context.Context, channelID string, expectedVersion int64) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE upstream_key_deliveries SET rotation_status=rotation_resume_status,rotation_resume_status='',updated_at=statement_timestamp()
		 WHERE channel_id=$1 AND key_version=$2 AND rotation_status='finalizing'
		   AND rotation_resume_status IN ('deploying','rolling_back')`, channelID, expectedVersion)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

func mapConflictOrNotFound(ctx context.Context, s *PostgresStore, channelID string, err error) error {
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var exists bool
	if queryErr := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM upstream_key_deliveries WHERE channel_id=$1)`, channelID).Scan(&exists); queryErr != nil {
		return queryErr
	}
	if exists {
		return ErrConflict
	}
	return ErrNotFound
}

func (s *PostgresStore) GetUpstreamInventory(ctx context.Context, poolID string) (contracts.UpstreamInventorySnapshot, error) {
	poolID = strings.TrimSpace(poolID)
	rows, err := s.pool.Query(ctx,
		`SELECT uc.id,uc.pool_id,uc.source_id,uc.account_ownership,uc.display_name,uc.provider,uc.models,
		        uc.probe_capability,uc.probe_endpoint_path,uc.groups,uc.credential_binding_id,uc.proxy_binding_id,
		        uc.priority,uc.weight,uc.cost_hint,uc.status,uc.inventory_state,uc.labels,uc.created_at,uc.updated_at,
		        a.user_id,a.first_plan_id,a.created_at,
		        d.id,d.masked_value,d.key_version,d.proof_status,d.proof_connector_id,d.proof_checked_at,d.created_at,d.updated_at
		   FROM upstream_channels uc
		   LEFT JOIN upstream_channel_allocations a ON a.channel_id=uc.id
		   LEFT JOIN upstream_key_deliveries d ON d.channel_id=uc.id
		  WHERE ($1='' OR uc.pool_id=$1) ORDER BY uc.created_at,uc.id`, poolID)
	if err != nil {
		return contracts.UpstreamInventorySnapshot{}, err
	}
	defer rows.Close()
	snapshot := contracts.UpstreamInventorySnapshot{AsOf: nowUTC()}
	for rows.Next() {
		var item contracts.UpstreamInventoryItem
		var models, groups, labels []byte
		var ownership, channelStatus string
		var allocationUserID *int64
		var firstPlanID *string
		var allocatedAt *time.Time
		var deliveryID, maskedValue, proofStatus, proofConnectorID *string
		var keyVersion *int64
		var proofCheckedAt, deliveryCreatedAt, deliveryUpdatedAt *time.Time
		if err := rows.Scan(
			&item.Channel.ID, &item.Channel.PoolID, &item.Channel.SourceID, &ownership, &item.Channel.DisplayName, &item.Channel.Provider, &models,
			&item.Channel.ProbeCapability, &item.Channel.ProbeEndpointPath, &groups, &item.Channel.CredentialBindingID, &item.Channel.ProxyBindingID,
			&item.Channel.Priority, &item.Channel.Weight, &item.Channel.CostHint, &channelStatus, &item.InventoryState, &labels,
			&item.Channel.CreatedAt, &item.Channel.UpdatedAt,
			&allocationUserID, &firstPlanID, &allocatedAt,
			&deliveryID, &maskedValue, &keyVersion, &proofStatus, &proofConnectorID, &proofCheckedAt, &deliveryCreatedAt, &deliveryUpdatedAt,
		); err != nil {
			return contracts.UpstreamInventorySnapshot{}, err
		}
		item.Channel.AccountOwnership = contracts.GatewayAccountOwnership(ownership).Normalize()
		item.Channel.Status = contracts.UpstreamChannelStatus(channelStatus)
		item.Channel.InventoryState = item.InventoryState
		item.Channel.Models, item.Channel.Groups, item.Channel.Labels = unmarshalStrings(models), unmarshalStrings(groups), unmarshalLabels(labels)
		if allocationUserID != nil {
			item.Allocated, item.AllocatedUserID, item.AllocatedAt = true, *allocationUserID, allocatedAt
			if firstPlanID != nil {
				item.FirstPlanID = *firstPlanID
			}
		}
		if deliveryID != nil {
			item.Delivery = &contracts.UpstreamKeyDelivery{
				ID: *deliveryID, ChannelID: item.Channel.ID, MaskedValue: valueOrEmpty(maskedValue), KeyVersion: valueOrZero(keyVersion),
				ProofStatus: contracts.DeliveryKeyProofStatus(valueOrEmpty(proofStatus)), ProofConnectorID: valueOrEmpty(proofConnectorID),
				ProofCheckedAt: proofCheckedAt, CreatedAt: valueOrTime(deliveryCreatedAt), UpdatedAt: valueOrTime(deliveryUpdatedAt),
			}
		}
		snapshot.Items = append(snapshot.Items, item)
	}
	if err := rows.Err(); err != nil {
		return contracts.UpstreamInventorySnapshot{}, err
	}
	if err := s.enrichInventoryFacts(ctx, poolID, &snapshot); err != nil {
		return contracts.UpstreamInventorySnapshot{}, err
	}
	return snapshot, nil
}

func (s *PostgresStore) enrichInventoryFacts(ctx context.Context, poolID string, snapshot *contracts.UpstreamInventorySnapshot) error {
	poolRows, err := s.pool.Query(ctx, `SELECT id,safety_stock_threshold FROM upstream_pools WHERE ($1='' OR id=$1) ORDER BY created_at,id`, poolID)
	if err != nil {
		return err
	}
	thresholds := make(map[string]int)
	for poolRows.Next() {
		var id string
		var threshold int
		if err := poolRows.Scan(&id, &threshold); err != nil {
			poolRows.Close()
			return err
		}
		thresholds[id] = threshold
	}
	poolRows.Close()
	for id, threshold := range thresholds {
		snapshot.Pools = append(snapshot.Pools, contracts.UpstreamPoolInventorySummary{PoolID: id, SafetyStockThreshold: threshold})
	}
	// Add summaries for any invalid/legacy orphan projection before retaining
	// element pointers; append may otherwise invalidate pointers on slice growth.
	for i := range snapshot.Items {
		item := &snapshot.Items[i]
		if _, ok := thresholds[item.Channel.PoolID]; !ok {
			thresholds[item.Channel.PoolID] = 0
			snapshot.Pools = append(snapshot.Pools, contracts.UpstreamPoolInventorySummary{PoolID: item.Channel.PoolID})
		}
	}
	summaryByPool := make(map[string]*contracts.UpstreamPoolInventorySummary, len(snapshot.Pools))
	for i := range snapshot.Pools {
		summaryByPool[snapshot.Pools[i].PoolID] = &snapshot.Pools[i]
	}
	itemByChannel := make(map[string]*contracts.UpstreamInventoryItem, len(snapshot.Items))
	for i := range snapshot.Items {
		item := &snapshot.Items[i]
		itemByChannel[item.Channel.ID] = item
		if _, ok := summaryByPool[item.Channel.PoolID]; !ok {
			continue
		}
		summary := summaryByPool[item.Channel.PoolID]
		summary.Total++
		if item.Allocated {
			summary.Allocated++
		}
		switch item.InventoryState {
		case contracts.UpstreamInventoryDraft:
			summary.Draft++
		case contracts.UpstreamInventoryTesting:
			summary.Testing++
		case contracts.UpstreamInventoryReady:
			summary.Ready++
			if !item.Allocated && item.Channel.Status == contracts.UpstreamChannelActive && item.Channel.AccountOwnership.Normalize() == contracts.GatewayAccountPlatformManaged {
				summary.Available++
			}
		case contracts.UpstreamInventoryQuarantined:
			summary.Quarantined++
		case contracts.UpstreamInventoryRetired:
			summary.Retired++
		}
		if item.Delivery != nil {
			if item.Delivery.ProofStatus == contracts.DeliveryKeyProofUnverified {
				summary.ProofUnverified++
			}
			if item.Delivery.ProofStatus == contracts.DeliveryKeyProofMismatch {
				summary.ProofMismatch++
			}
		}
	}
	factRows, err := s.pool.Query(ctx, `SELECT DISTINCT pb.channel_id,COALESCE(NULLIF(BTRIM(pb.instance_id),''),rp.instance_id),p.status,d.status
		FROM published_bindings pb
		JOIN route_plans rp ON rp.id=pb.plan_id
		LEFT JOIN upstream_key_proof_receipts p ON p.channel_id=pb.channel_id AND p.instance_id=COALESCE(NULLIF(BTRIM(pb.instance_id),''),rp.instance_id)
		LEFT JOIN upstream_key_deployments d ON d.channel_id=pb.channel_id AND d.instance_id=COALESCE(NULLIF(BTRIM(pb.instance_id),''),rp.instance_id)
		WHERE pb.state<>'revoked'`)
	if err != nil {
		return err
	}
	defer factRows.Close()
	for factRows.Next() {
		var channelID, instanceID string
		var proof *string
		var deployment *string
		if err := factRows.Scan(&channelID, &instanceID, &proof, &deployment); err != nil {
			return err
		}
		item := itemByChannel[channelID]
		if item == nil {
			continue
		}
		item.TargetInstances++
		if proof != nil && *proof == "verified" {
			item.ProofVerified++
		}
		if proof != nil && *proof == "mismatch" {
			item.ProofMismatch++
		}
		if deployment != nil {
			switch *deployment {
			case "deployed":
				item.DeploymentsDeployed++
			case "pending":
				item.DeploymentsPending++
			case "failed":
				item.DeploymentsFailed++
				summaryByPool[item.Channel.PoolID].DeploymentsFailed++
			}
		}
	}
	if err := factRows.Err(); err != nil {
		return err
	}
	sort.Slice(snapshot.Pools, func(i, j int) bool { return snapshot.Pools[i].PoolID < snapshot.Pools[j].PoolID })
	for i := range snapshot.Pools {
		summary := &snapshot.Pools[i]
		summary.BelowSafetyStock = summary.SafetyStockThreshold > 0 && summary.Available < summary.SafetyStockThreshold
		if summary.BelowSafetyStock {
			snapshot.Alerts = append(snapshot.Alerts, contracts.UpstreamInventoryAlert{PoolID: summary.PoolID, Code: "safety_stock_low", Message: fmt.Sprintf("available inventory %d is below safety stock %d", summary.Available, summary.SafetyStockThreshold), Available: summary.Available, Threshold: summary.SafetyStockThreshold})
		}
	}
	return nil
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
func valueOrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
func valueOrTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

func (s *PostgresStore) CreatePoolRetirementJob(ctx context.Context, poolID string, createdBy int64) (contracts.PoolRetirementJob, error) {
	poolID = strings.TrimSpace(poolID)
	if poolID == "" {
		return contracts.PoolRetirementJob{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.PoolRetirementJob{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var previousStatus string
	if err := tx.QueryRow(ctx, `UPDATE upstream_pools SET status='maintenance',updated_at=statement_timestamp() WHERE id=$1 AND status<>'retired' RETURNING status`, poolID).Scan(&previousStatus); err != nil {
		return contracts.PoolRetirementJob{}, mapNotFound(err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE pool_rollout_operations
		    SET status='superseded',last_error='pool retirement started',version=version+1,
		        lease_owner='',lease_until=NULL,updated_at=statement_timestamp()
		  WHERE pool_id=$1 AND action='publish' AND status IN ('pending','failed','running')`, poolID); err != nil {
		return contracts.PoolRetirementJob{}, err
	}
	job := contracts.PoolRetirementJob{ID: newID("poolret"), PoolID: poolID, Status: contracts.PoolRetirementPending, CreatedBy: createdBy}
	err = tx.QueryRow(ctx, `INSERT INTO pool_retirement_jobs(id,pool_id,status,created_by,created_at,updated_at) VALUES($1,$2,'pending',$3,statement_timestamp(),statement_timestamp()) RETURNING created_at,updated_at`, job.ID, poolID, createdBy).Scan(&job.CreatedAt, &job.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return contracts.PoolRetirementJob{}, ErrConflict
		}
		return contracts.PoolRetirementJob{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO pool_retirement_items(job_id,plan_id,instance_id,status,updated_at) SELECT $1,id,instance_id,'pending',statement_timestamp() FROM route_plans WHERE pool_id=$2`, job.ID, poolID); err != nil {
		return contracts.PoolRetirementJob{}, err
	}
	if err := tx.QueryRow(ctx,
		`UPDATE pool_retirement_jobs j
		    SET total_plans=items.total,
		        status=CASE WHEN items.total=0 THEN 'finalizing' ELSE 'pending' END,
		        updated_at=statement_timestamp()
		   FROM (SELECT COUNT(*)::integer AS total
		           FROM pool_retirement_items WHERE job_id=$1) items
		  WHERE j.id=$1
		  RETURNING j.total_plans,j.status,j.updated_at`, job.ID,
	).Scan(&job.TotalPlans, &job.Status, &job.UpdatedAt); err != nil {
		return contracts.PoolRetirementJob{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.PoolRetirementJob{}, err
	}
	return s.GetPoolRetirementJob(ctx, job.ID)
}

func (s *PostgresStore) GetPoolRetirementJob(ctx context.Context, id string) (contracts.PoolRetirementJob, error) {
	var job contracts.PoolRetirementJob
	err := s.pool.QueryRow(ctx, `SELECT id,pool_id,status,total_plans,completed_plans,failed_plans,cleanup_completed_plans,cleanup_failed_plans,last_error,created_by,created_at,updated_at,completed_at FROM pool_retirement_jobs WHERE id=$1`, id).Scan(
		&job.ID, &job.PoolID, &job.Status, &job.TotalPlans, &job.CompletedPlans, &job.FailedPlans,
		&job.CleanupCompletedPlans, &job.CleanupFailedPlans, &job.LastError, &job.CreatedBy,
		&job.CreatedAt, &job.UpdatedAt, &job.CompletedAt,
	)
	if err != nil {
		return contracts.PoolRetirementJob{}, mapNotFound(err)
	}
	rows, err := s.pool.Query(ctx, `SELECT job_id,plan_id,instance_id,status,last_error,attempts,lease_until,cleanup_status,cleanup_last_error,cleanup_attempts,cleanup_lease_until,updated_at FROM pool_retirement_items WHERE job_id=$1 ORDER BY plan_id`, id)
	if err != nil {
		return contracts.PoolRetirementJob{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var item contracts.PoolRetirementItem
		if err := rows.Scan(
			&item.JobID, &item.PlanID, &item.InstanceID, &item.Status, &item.LastError, &item.Attempts, &item.LeaseUntil,
			&item.CleanupStatus, &item.CleanupLastError, &item.CleanupAttempts, &item.CleanupLeaseUntil, &item.UpdatedAt,
		); err != nil {
			return contracts.PoolRetirementJob{}, err
		}
		job.Items = append(job.Items, item)
	}
	return job, rows.Err()
}

func (s *PostgresStore) ListPoolRetirementJobs(ctx context.Context, poolID string) ([]contracts.PoolRetirementJob, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM pool_retirement_jobs WHERE ($1='' OR pool_id=$1) ORDER BY created_at DESC,id DESC`, strings.TrimSpace(poolID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]contracts.PoolRetirementJob, 0, len(ids))
	for _, id := range ids {
		job, err := s.GetPoolRetirementJob(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, job)
	}
	return out, nil
}

func (s *PostgresStore) ClaimPoolRetirementItem(ctx context.Context, jobID string) (contracts.PoolRetirementItem, bool, error) {
	var item contracts.PoolRetirementItem
	err := s.pool.QueryRow(ctx, `WITH candidate AS (SELECT job_id,plan_id FROM pool_retirement_items WHERE job_id=$1 AND status IN ('pending','failed','running') AND (lease_until IS NULL OR lease_until<=statement_timestamp()) ORDER BY CASE status WHEN 'pending' THEN 0 WHEN 'failed' THEN 1 ELSE 2 END,plan_id FOR UPDATE SKIP LOCKED LIMIT 1) UPDATE pool_retirement_items i SET status='running',last_error='',attempts=i.attempts+1,lease_until=statement_timestamp()+interval '2 minutes',updated_at=statement_timestamp() FROM candidate c WHERE i.job_id=c.job_id AND i.plan_id=c.plan_id RETURNING i.job_id,i.plan_id,i.instance_id,i.status,i.last_error,i.attempts,i.lease_until,i.updated_at`, jobID).Scan(&item.JobID, &item.PlanID, &item.InstanceID, &item.Status, &item.LastError, &item.Attempts, &item.LeaseUntil, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return contracts.PoolRetirementItem{}, false, nil
	}
	if err != nil {
		return contracts.PoolRetirementItem{}, false, err
	}
	_, _ = s.pool.Exec(ctx, `UPDATE pool_retirement_jobs SET status='running',updated_at=statement_timestamp() WHERE id=$1`, jobID)
	return item, true, nil
}

func (s *PostgresStore) RenewPoolRetirementItem(ctx context.Context, jobID, planID string, expectedAttempts int, lease time.Duration) (contracts.PoolRetirementItem, error) {
	if strings.TrimSpace(jobID) == "" || strings.TrimSpace(planID) == "" || expectedAttempts <= 0 || lease <= 0 || lease.Microseconds() <= 0 {
		return contracts.PoolRetirementItem{}, ErrInvalid
	}
	var item contracts.PoolRetirementItem
	err := s.pool.QueryRow(ctx,
		`UPDATE pool_retirement_items
		    SET lease_until=statement_timestamp()+($4::bigint * interval '1 microsecond'),
		        updated_at=statement_timestamp()
		  WHERE job_id=$1 AND plan_id=$2 AND status='running' AND attempts=$3
		    AND lease_until IS NOT NULL AND lease_until>statement_timestamp()
		 RETURNING job_id,plan_id,instance_id,status,last_error,attempts,lease_until,updated_at`,
		jobID, planID, expectedAttempts, lease.Microseconds()).Scan(
		&item.JobID, &item.PlanID, &item.InstanceID, &item.Status, &item.LastError,
		&item.Attempts, &item.LeaseUntil, &item.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return contracts.PoolRetirementItem{}, ErrConflict
	}
	return item, err
}

func (s *PostgresStore) CompletePoolRetirementItem(ctx context.Context, jobID, planID string, expectedAttempts int, errorMessage string) (contracts.PoolRetirementJob, error) {
	if strings.TrimSpace(jobID) == "" || strings.TrimSpace(planID) == "" || expectedAttempts <= 0 {
		return contracts.PoolRetirementJob{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.PoolRetirementJob{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	status := "completed"
	if strings.TrimSpace(errorMessage) != "" {
		status = "failed"
	}
	tag, err := tx.Exec(ctx, `UPDATE pool_retirement_items SET status=$4,last_error=$5,lease_until=NULL,updated_at=statement_timestamp() WHERE job_id=$1 AND plan_id=$2 AND status='running' AND attempts=$3 AND lease_until IS NOT NULL AND lease_until>statement_timestamp()`, jobID, planID, expectedAttempts, status, strings.TrimSpace(errorMessage))
	if err != nil {
		return contracts.PoolRetirementJob{}, err
	}
	if tag.RowsAffected() == 0 {
		return contracts.PoolRetirementJob{}, ErrConflict
	}
	_, err = tx.Exec(ctx, `UPDATE pool_retirement_jobs j SET total_plans=s.total,completed_plans=s.completed,failed_plans=s.failed,last_error=s.last_error,status=CASE WHEN s.pending>0 THEN 'running' WHEN s.failed>0 THEN 'partial' ELSE 'finalizing' END,completed_at=NULL,updated_at=statement_timestamp() FROM (SELECT job_id,COUNT(*) total,COUNT(*) FILTER(WHERE status='completed') completed,COUNT(*) FILTER(WHERE status='failed') failed,COUNT(*) FILTER(WHERE status IN('pending','running')) pending,COALESCE(MAX(last_error) FILTER(WHERE status='failed'),'') last_error FROM pool_retirement_items WHERE job_id=$1 GROUP BY job_id)s WHERE j.id=s.job_id`, jobID)
	if err != nil {
		return contracts.PoolRetirementJob{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.PoolRetirementJob{}, err
	}
	return s.GetPoolRetirementJob(ctx, jobID)
}

func (s *PostgresStore) FinalizePoolRetirementJob(ctx context.Context, jobID string) (contracts.PoolRetirementJob, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.PoolRetirementJob{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var poolID string
	err = tx.QueryRow(ctx,
		`SELECT pool_id FROM pool_retirement_jobs
		  WHERE id=$1 AND status='finalizing' AND failed_plans=0
		    AND (total_plans=0 OR completed_plans=total_plans) FOR UPDATE`, jobID,
	).Scan(&poolID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return contracts.PoolRetirementJob{}, err
		}
		var exists bool
		if queryErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pool_retirement_jobs WHERE id=$1)`, jobID).Scan(&exists); queryErr != nil {
			return contracts.PoolRetirementJob{}, queryErr
		}
		if exists {
			return contracts.PoolRetirementJob{}, ErrConflict
		}
		return contracts.PoolRetirementJob{}, ErrNotFound
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('e2m.pool_retirement_job','allowed',true)`); err != nil {
		return contracts.PoolRetirementJob{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE upstream_pools SET status='retired',updated_at=statement_timestamp() WHERE id=$1 AND status='maintenance'`, poolID)
	if err != nil {
		return contracts.PoolRetirementJob{}, err
	}
	if tag.RowsAffected() == 0 {
		return contracts.PoolRetirementJob{}, ErrConflict
	}
	if _, err := tx.Exec(ctx,
		`UPDATE pool_retirement_jobs
		    SET status=CASE WHEN total_plans=0 THEN 'completed' ELSE 'cleanup' END,
		        cleanup_completed_plans=0,cleanup_failed_plans=0,last_error='',
		        completed_at=CASE WHEN total_plans=0 THEN statement_timestamp() ELSE NULL END,
		        updated_at=statement_timestamp()
		  WHERE id=$1`, jobID); err != nil {
		return contracts.PoolRetirementJob{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.PoolRetirementJob{}, err
	}
	return s.GetPoolRetirementJob(ctx, jobID)
}

func (s *PostgresStore) ClaimPoolRetirementCleanupItem(ctx context.Context, jobID string) (contracts.PoolRetirementItem, bool, error) {
	var item contracts.PoolRetirementItem
	err := s.pool.QueryRow(ctx,
		`WITH candidate AS (
		   SELECT item.job_id,item.plan_id
		     FROM pool_retirement_items item
		     JOIN pool_retirement_jobs job ON job.id=item.job_id
		    WHERE item.job_id=$1 AND job.status='cleanup' AND item.status='completed'
		      AND item.cleanup_status IN ('pending','failed','running')
		      AND (item.cleanup_lease_until IS NULL OR item.cleanup_lease_until<=statement_timestamp())
		    ORDER BY CASE item.cleanup_status WHEN 'pending' THEN 0 WHEN 'failed' THEN 1 ELSE 2 END,item.plan_id
		    FOR UPDATE OF item SKIP LOCKED LIMIT 1
		 )
		 UPDATE pool_retirement_items item
		    SET cleanup_status='running',cleanup_last_error='',cleanup_attempts=item.cleanup_attempts+1,
		        cleanup_lease_until=statement_timestamp()+interval '2 minutes',updated_at=statement_timestamp()
		   FROM candidate
		  WHERE item.job_id=candidate.job_id AND item.plan_id=candidate.plan_id
		 RETURNING item.job_id,item.plan_id,item.instance_id,item.status,item.last_error,item.attempts,item.lease_until,
		           item.cleanup_status,item.cleanup_last_error,item.cleanup_attempts,item.cleanup_lease_until,item.updated_at`, jobID).Scan(
		&item.JobID, &item.PlanID, &item.InstanceID, &item.Status, &item.LastError, &item.Attempts, &item.LeaseUntil,
		&item.CleanupStatus, &item.CleanupLastError, &item.CleanupAttempts, &item.CleanupLeaseUntil, &item.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return contracts.PoolRetirementItem{}, false, nil
	}
	if err != nil {
		return contracts.PoolRetirementItem{}, false, err
	}
	_, _ = s.pool.Exec(ctx, `UPDATE pool_retirement_jobs SET updated_at=statement_timestamp() WHERE id=$1 AND status='cleanup'`, jobID)
	return item, true, nil
}

func (s *PostgresStore) RenewPoolRetirementCleanupItem(ctx context.Context, jobID, planID string, expectedAttempts int, lease time.Duration) (contracts.PoolRetirementItem, error) {
	if strings.TrimSpace(jobID) == "" || strings.TrimSpace(planID) == "" || expectedAttempts <= 0 || lease <= 0 || lease.Microseconds() <= 0 {
		return contracts.PoolRetirementItem{}, ErrInvalid
	}
	var item contracts.PoolRetirementItem
	err := s.pool.QueryRow(ctx,
		`UPDATE pool_retirement_items item
		    SET cleanup_lease_until=statement_timestamp()+($4::bigint * interval '1 microsecond'),
		        updated_at=statement_timestamp()
		   FROM pool_retirement_jobs job
		  WHERE item.job_id=$1 AND item.plan_id=$2 AND item.cleanup_attempts=$3
		    AND item.job_id=job.id AND job.status='cleanup' AND item.status='completed'
		    AND item.cleanup_status='running' AND item.cleanup_lease_until IS NOT NULL
		    AND item.cleanup_lease_until>statement_timestamp()
		 RETURNING item.job_id,item.plan_id,item.instance_id,item.status,item.last_error,item.attempts,item.lease_until,
		           item.cleanup_status,item.cleanup_last_error,item.cleanup_attempts,item.cleanup_lease_until,item.updated_at`,
		jobID, planID, expectedAttempts, lease.Microseconds()).Scan(
		&item.JobID, &item.PlanID, &item.InstanceID, &item.Status, &item.LastError, &item.Attempts, &item.LeaseUntil,
		&item.CleanupStatus, &item.CleanupLastError, &item.CleanupAttempts, &item.CleanupLeaseUntil, &item.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return contracts.PoolRetirementItem{}, ErrConflict
	}
	return item, err
}

func (s *PostgresStore) CompletePoolRetirementCleanupItem(ctx context.Context, jobID, planID string, expectedAttempts int, errorMessage string) (contracts.PoolRetirementJob, error) {
	if strings.TrimSpace(jobID) == "" || strings.TrimSpace(planID) == "" || expectedAttempts <= 0 {
		return contracts.PoolRetirementJob{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.PoolRetirementJob{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	status := string(contracts.PoolRetirementCleanupCompleted)
	if strings.TrimSpace(errorMessage) != "" {
		status = string(contracts.PoolRetirementCleanupFailed)
	}
	tag, err := tx.Exec(ctx,
		`UPDATE pool_retirement_items
		    SET cleanup_status=$4,cleanup_last_error=$5,cleanup_lease_until=NULL,updated_at=statement_timestamp()
		  WHERE job_id=$1 AND plan_id=$2 AND cleanup_status='running' AND cleanup_attempts=$3
		    AND cleanup_lease_until IS NOT NULL AND cleanup_lease_until>statement_timestamp()`,
		jobID, planID, expectedAttempts, status, strings.TrimSpace(errorMessage))
	if err != nil {
		return contracts.PoolRetirementJob{}, err
	}
	if tag.RowsAffected() == 0 {
		return contracts.PoolRetirementJob{}, ErrConflict
	}
	tag, err = tx.Exec(ctx,
		`UPDATE pool_retirement_jobs job
		    SET cleanup_completed_plans=summary.completed,
		        cleanup_failed_plans=summary.failed,
		        last_error=summary.last_error,
		        status=CASE WHEN summary.pending=0 AND summary.failed=0 THEN 'completed' ELSE 'cleanup' END,
		        completed_at=CASE WHEN summary.pending=0 AND summary.failed=0 THEN statement_timestamp() ELSE NULL END,
		        updated_at=statement_timestamp()
		   FROM (
		     SELECT job_id,
		            COUNT(*) FILTER(WHERE cleanup_status='completed')::integer completed,
		            COUNT(*) FILTER(WHERE cleanup_status='failed')::integer failed,
		            COUNT(*) FILTER(WHERE cleanup_status IN('pending','running'))::integer pending,
		            COALESCE(MAX(cleanup_last_error) FILTER(WHERE cleanup_status='failed'),'') last_error
		       FROM pool_retirement_items WHERE job_id=$1 GROUP BY job_id
		   ) summary
		  WHERE job.id=summary.job_id AND job.status='cleanup'`, jobID)
	if err != nil {
		return contracts.PoolRetirementJob{}, err
	}
	if tag.RowsAffected() == 0 {
		return contracts.PoolRetirementJob{}, ErrConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.PoolRetirementJob{}, err
	}
	return s.GetPoolRetirementJob(ctx, jobID)
}
