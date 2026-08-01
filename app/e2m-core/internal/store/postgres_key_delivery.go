package store

import (
	"context"
	"errors"

	"e2m.local/contracts"
)

func (s *PostgresStore) UpsertUpstreamKeyDelivery(ctx context.Context, input contracts.UpstreamKeyDelivery) (contracts.UpstreamKeyDelivery, error) {
	if input.ID == "" {
		input.ID = newID("keydel")
	}
	now := nowUTC()
	input.CreatedAt, input.UpdatedAt = now, now
	row := s.pool.QueryRow(ctx,
		`INSERT INTO upstream_key_deliveries (id, channel_id, secret_ref, masked_value, key_version, proof_status, proof_connector_id, proof_checked_at, created_at, updated_at)
		 SELECT $1, uc.id, $3, $4, 1, 'unverified', '', NULL, $5, $6
		   FROM upstream_channels uc
		  WHERE uc.id=$2 AND uc.account_ownership='platform_managed'
		 ON CONFLICT (channel_id) DO UPDATE SET
		   secret_ref=EXCLUDED.secret_ref, masked_value=EXCLUDED.masked_value,
		   key_version=upstream_key_deliveries.key_version+1,
		   proof_status='unverified', proof_connector_id='', proof_checked_at=NULL,
		   updated_at=EXCLUDED.updated_at
		 RETURNING id, key_version, proof_status, proof_connector_id, proof_checked_at, created_at, updated_at`,
		input.ID, input.ChannelID, input.SecretRef, input.MaskedValue, input.CreatedAt, input.UpdatedAt)
	if err := row.Scan(&input.ID, &input.KeyVersion, &input.ProofStatus, &input.ProofConnectorID, &input.ProofCheckedAt, &input.CreatedAt, &input.UpdatedAt); err != nil {
		if !errors.Is(err, pgxNoRows()) {
			return contracts.UpstreamKeyDelivery{}, err
		}
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM upstream_channels WHERE id=$1)`, input.ChannelID).Scan(&exists); err != nil {
			return contracts.UpstreamKeyDelivery{}, err
		}
		if exists {
			return contracts.UpstreamKeyDelivery{}, ErrConflict
		}
		return contracts.UpstreamKeyDelivery{}, ErrNotFound
	}
	return input, nil
}

func (s *PostgresStore) GetUpstreamKeyDelivery(ctx context.Context, channelID string) (contracts.UpstreamKeyDelivery, error) {
	var delivery contracts.UpstreamKeyDelivery
	err := s.pool.QueryRow(ctx,
		`SELECT id, channel_id, secret_ref, masked_value, key_version, proof_status, proof_connector_id, proof_checked_at, created_at, updated_at
		   FROM upstream_key_deliveries WHERE channel_id=$1`, channelID,
	).Scan(&delivery.ID, &delivery.ChannelID, &delivery.SecretRef, &delivery.MaskedValue, &delivery.KeyVersion, &delivery.ProofStatus, &delivery.ProofConnectorID, &delivery.ProofCheckedAt, &delivery.CreatedAt, &delivery.UpdatedAt)
	if err != nil {
		return contracts.UpstreamKeyDelivery{}, mapNotFound(err)
	}
	return delivery, nil
}

func (s *PostgresStore) ListUpstreamKeyDeliveries(ctx context.Context) ([]contracts.UpstreamKeyDelivery, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, channel_id, secret_ref, masked_value, key_version, proof_status, proof_connector_id, proof_checked_at, created_at, updated_at
		   FROM upstream_key_deliveries ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]contracts.UpstreamKeyDelivery, 0)
	for rows.Next() {
		var delivery contracts.UpstreamKeyDelivery
		if err := rows.Scan(&delivery.ID, &delivery.ChannelID, &delivery.SecretRef, &delivery.MaskedValue, &delivery.KeyVersion, &delivery.ProofStatus, &delivery.ProofConnectorID, &delivery.ProofCheckedAt, &delivery.CreatedAt, &delivery.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, delivery)
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpdateUpstreamKeyDeliveryProof(ctx context.Context, channelID string, expectedKeyVersion int64, connectorID string, status contracts.DeliveryKeyProofStatus) (contracts.UpstreamKeyDelivery, error) {
	if expectedKeyVersion <= 0 || connectorID == "" || !status.Valid() {
		return contracts.UpstreamKeyDelivery{}, ErrConflict
	}
	var delivery contracts.UpstreamKeyDelivery
	err := s.pool.QueryRow(ctx,
		`UPDATE upstream_key_deliveries
		    SET proof_status=$3, proof_connector_id=$4, proof_checked_at=now()
		  WHERE channel_id=$1 AND key_version=$2
		  RETURNING id, channel_id, secret_ref, masked_value, key_version, proof_status, proof_connector_id, proof_checked_at, created_at, updated_at`,
		channelID, expectedKeyVersion, string(status), connectorID,
	).Scan(&delivery.ID, &delivery.ChannelID, &delivery.SecretRef, &delivery.MaskedValue, &delivery.KeyVersion, &delivery.ProofStatus, &delivery.ProofConnectorID, &delivery.ProofCheckedAt, &delivery.CreatedAt, &delivery.UpdatedAt)
	if err == nil {
		return delivery, nil
	}
	if !errors.Is(err, pgxNoRows()) {
		return contracts.UpstreamKeyDelivery{}, err
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM upstream_key_deliveries WHERE channel_id=$1)`, channelID).Scan(&exists); err != nil {
		return contracts.UpstreamKeyDelivery{}, err
	}
	if !exists {
		return contracts.UpstreamKeyDelivery{}, ErrNotFound
	}
	return contracts.UpstreamKeyDelivery{}, ErrConflict
}

func (s *PostgresStore) GetUpstreamKeyProofReceipt(ctx context.Context, channelID, instanceID string) (contracts.UpstreamKeyProofReceipt, error) {
	var receipt contracts.UpstreamKeyProofReceipt
	err := s.pool.QueryRow(ctx,
		`SELECT channel_id, instance_id, key_version, connector_id, status, checked_at
		   FROM upstream_key_proof_receipts WHERE channel_id=$1 AND instance_id=$2`, channelID, instanceID,
	).Scan(&receipt.ChannelID, &receipt.InstanceID, &receipt.KeyVersion, &receipt.ConnectorID, &receipt.Status, &receipt.CheckedAt)
	if err != nil {
		return contracts.UpstreamKeyProofReceipt{}, mapNotFound(err)
	}
	return receipt, nil
}

func (s *PostgresStore) UpsertUpstreamKeyProofReceipt(ctx context.Context, input contracts.UpstreamKeyProofReceipt) (contracts.UpstreamKeyProofReceipt, error) {
	if input.ChannelID == "" || input.InstanceID == "" || input.ConnectorID == "" || input.KeyVersion <= 0 || !input.Status.Valid() {
		return contracts.UpstreamKeyProofReceipt{}, ErrConflict
	}
	err := s.pool.QueryRow(ctx,
		`INSERT INTO upstream_key_proof_receipts
		 (channel_id, instance_id, key_version, connector_id, status, checked_at)
		 SELECT $1, i.id, $3, $4, $5, statement_timestamp()
		   FROM instances i
		   JOIN upstream_key_deliveries d ON d.channel_id=$1 AND d.key_version=$3
		  WHERE i.id=$2
		 ON CONFLICT (channel_id, instance_id) DO UPDATE SET
		   key_version=EXCLUDED.key_version, connector_id=EXCLUDED.connector_id,
		   status=EXCLUDED.status, checked_at=EXCLUDED.checked_at
		 RETURNING channel_id, instance_id, key_version, connector_id, status, checked_at`,
		input.ChannelID, input.InstanceID, input.KeyVersion, input.ConnectorID, string(input.Status),
	).Scan(&input.ChannelID, &input.InstanceID, &input.KeyVersion, &input.ConnectorID, &input.Status, &input.CheckedAt)
	if err == nil {
		return input, nil
	}
	if !errors.Is(err, pgxNoRows()) {
		return contracts.UpstreamKeyProofReceipt{}, err
	}
	var deliveryExists, instanceExists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM upstream_key_deliveries WHERE channel_id=$1)`, input.ChannelID).Scan(&deliveryExists); err != nil {
		return contracts.UpstreamKeyProofReceipt{}, err
	}
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM instances WHERE id=$1)`, input.InstanceID).Scan(&instanceExists); err != nil {
		return contracts.UpstreamKeyProofReceipt{}, err
	}
	if !deliveryExists || !instanceExists {
		return contracts.UpstreamKeyProofReceipt{}, ErrNotFound
	}
	return contracts.UpstreamKeyProofReceipt{}, ErrConflict
}

func (s *PostgresStore) GetUpstreamKeyDeployment(ctx context.Context, channelID, instanceID string) (contracts.UpstreamKeyDeployment, error) {
	var deployment contracts.UpstreamKeyDeployment
	err := s.pool.QueryRow(ctx,
		`SELECT channel_id, instance_id, key_version, connector_id, status, deployed_at, updated_at
		   FROM upstream_key_deployments WHERE channel_id=$1 AND instance_id=$2`, channelID, instanceID,
	).Scan(&deployment.ChannelID, &deployment.InstanceID, &deployment.KeyVersion, &deployment.ConnectorID,
		&deployment.Status, &deployment.DeployedAt, &deployment.UpdatedAt)
	if err != nil {
		return contracts.UpstreamKeyDeployment{}, mapNotFound(err)
	}
	return deployment, nil
}

func (s *PostgresStore) UpsertUpstreamKeyDeployment(ctx context.Context, input contracts.UpstreamKeyDeployment) (contracts.UpstreamKeyDeployment, error) {
	if input.ChannelID == "" || input.InstanceID == "" || input.ConnectorID == "" || input.KeyVersion <= 0 || !input.Status.Valid() {
		return contracts.UpstreamKeyDeployment{}, ErrConflict
	}
	err := s.pool.QueryRow(ctx,
		`INSERT INTO upstream_key_deployments (channel_id, instance_id, key_version, connector_id, status, deployed_at, updated_at)
		 SELECT $1, $2, $3, $4, $5,
		        CASE WHEN $5='deployed' THEN now() ELSE NULL END, now()
		   FROM upstream_key_deliveries d
		  WHERE d.channel_id=$1 AND d.key_version=$3
		 ON CONFLICT (channel_id, instance_id) DO UPDATE SET
		   key_version=EXCLUDED.key_version, connector_id=EXCLUDED.connector_id,
		   status=EXCLUDED.status, deployed_at=EXCLUDED.deployed_at, updated_at=EXCLUDED.updated_at
		 RETURNING channel_id, instance_id, key_version, connector_id, status, deployed_at, updated_at`,
		input.ChannelID, input.InstanceID, input.KeyVersion, input.ConnectorID, string(input.Status),
	).Scan(&input.ChannelID, &input.InstanceID, &input.KeyVersion, &input.ConnectorID, &input.Status, &input.DeployedAt, &input.UpdatedAt)
	if err == nil {
		return input, nil
	}
	if !errors.Is(err, pgxNoRows()) {
		return contracts.UpstreamKeyDeployment{}, err
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM upstream_key_deliveries WHERE channel_id=$1)`, input.ChannelID).Scan(&exists); err != nil {
		return contracts.UpstreamKeyDeployment{}, err
	}
	if !exists {
		return contracts.UpstreamKeyDeployment{}, ErrNotFound
	}
	return contracts.UpstreamKeyDeployment{}, ErrConflict
}

func (s *PostgresStore) ListAssignedUpstreamKeys(ctx context.Context, userID int64) ([]contracts.AssignedUpstreamKey, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT d.id, uc.display_name, uc.provider, d.masked_value, d.key_version,
		        d.proof_status, d.proof_connector_id, d.proof_checked_at,
		        a.created_at, d.channel_id, d.secret_ref
		   FROM upstream_channel_allocations a
		   JOIN upstream_channels uc ON uc.id=a.channel_id
		   JOIN upstream_key_deliveries d ON d.channel_id=a.channel_id
		  WHERE a.user_id=$1 AND uc.account_ownership='platform_managed'
		  ORDER BY a.created_at, d.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]contracts.AssignedUpstreamKey, 0)
	for rows.Next() {
		var key contracts.AssignedUpstreamKey
		if err := rows.Scan(&key.ID, &key.DisplayName, &key.Provider, &key.MaskedValue, &key.KeyVersion,
			&key.ProofStatus, &key.ProofConnectorID, &key.ProofCheckedAt,
			&key.AllocatedAt, &key.ChannelID, &key.SecretRef); err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, rows.Err()
}
