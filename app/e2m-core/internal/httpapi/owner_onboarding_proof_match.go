package httpapi

import "e2m.local/contracts"

func ownerProofMatches(
	proof contracts.UpstreamKeyProofReceipt,
	currentConnectorID string,
	workflowConnectorID string,
	keyVersion int64,
) bool {
	return currentConnectorID != "" && workflowConnectorID == currentConnectorID &&
		proof.ConnectorID == currentConnectorID && proof.KeyVersion == keyVersion &&
		proof.Status == contracts.DeliveryKeyProofVerified
}
