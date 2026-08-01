import type { IntelligenceLink, IntelligenceLinkInput } from '../../api/upstreamIntelligence'

export function intelligenceLinkStatusInput(
  link: IntelligenceLink,
  userId: number,
  active: boolean,
): IntelligenceLinkInput {
  return {
    id: link.id,
    user_id: userId,
    intelligence_source_id: link.intelligence_source_id,
    link_scope: link.link_scope,
    price_dimension: link.price_dimension,
    status: active ? 'active' : 'inactive',
  }
}
