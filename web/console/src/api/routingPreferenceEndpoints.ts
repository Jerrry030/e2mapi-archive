import { apiClient } from './client'
import type { OwnerRoutingPreference, OwnerRoutingPreferenceResult } from './routingPreferenceTypes'

export const routingPreferenceEndpoints = {
  get: () => apiClient.request<OwnerRoutingPreferenceResult>('/owner/routing-preference'),
  update: (preference: OwnerRoutingPreference) =>
    apiClient.request<OwnerRoutingPreferenceResult>('/owner/routing-preference', {
      method: 'PUT',
      body: { preference },
    }),
}
