import type { DeliveryKeyProofStatus } from '../api/types'

export function deliveryKeyProofView(status: DeliveryKeyProofStatus | undefined) {
  switch (status) {
    case 'verified':
      return { label: '本地绑定已匹配', color: 'success' as const }
    case 'mismatch':
      return { label: 'Key 不一致', color: 'error' as const }
    default:
      return { label: '待校验', color: 'default' as const }
  }
}
