import { describe, expect, it } from 'vitest'
import type { IntelligenceLink } from '../../api/upstreamIntelligence'
import { intelligenceLinkStatusInput } from './intelligenceLinkInput'

const sourceIdentityLink: IntelligenceLink = {
  id: 'link-1',
  intelligence_source_id: 'source-1',
  link_scope: 'source_identity',
  channel_id: 'resolved-channel-1',
  price_dimension: 'input',
  status: 'active',
  verified_at: '2026-07-24T00:00:00Z',
  created_at: '2026-07-24T00:00:00Z',
  updated_at: '2026-07-24T00:00:00Z',
}

describe('intelligenceLinkStatusInput', () => {
  it('does not turn a resolved channel into the target of a source-identity link', () => {
    expect(intelligenceLinkStatusInput(sourceIdentityLink, 7, false)).toEqual({
      id: 'link-1',
      user_id: 7,
      intelligence_source_id: 'source-1',
      link_scope: 'source_identity',
      price_dimension: 'input',
      status: 'inactive',
    })
  })
})
