import { describe, expect, it } from 'vitest'
import { deliveryKeyProofView } from './deliveryKeyProofView'

describe('delivery key local binding proof presentation', () => {
  it.each([
    ['verified', '本地绑定已匹配', 'success'],
    ['unverified', '待校验', 'default'],
    ['mismatch', 'Key 不一致', 'error'],
  ] as const)('renders %s without claiming remote attestation', (status, label, color) => {
    expect(deliveryKeyProofView(status)).toEqual({ label, color })
  })
})
