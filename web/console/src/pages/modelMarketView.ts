import type {
  OwnerModelMarketModel,
  OwnerModelMarketPrice,
  OwnerModelMarketStatus,
} from '../api/ownerModelMarket'

export type ModelMarketSort = 'recommended' | 'lowest_price' | 'quality' | 'success' | 'latency'

export interface ModelMarketPriceCohort {
  dimension: OwnerModelMarketPrice['dimension']
  currency: string
  perTokens: number
}

export interface ModelMarketViewFilter {
  query?: string
  status?: OwnerModelMarketStatus
  sort?: ModelMarketSort
  priceCohort?: ModelMarketPriceCohort
}

function decimal(value: string | null | undefined): number | undefined {
  if (value === null || value === undefined || !/^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?$/.test(value)) {
    return undefined
  }
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : undefined
}

export function modelMinimumPrice(
  model: OwnerModelMarketModel,
  priceCohort?: ModelMarketPriceCohort,
): number | undefined {
  const values = model.prices
    .filter(
      (price) =>
        !priceCohort ||
        (price.dimension === priceCohort.dimension &&
          price.currency === priceCohort.currency &&
          price.per_tokens === priceCohort.perTokens),
    )
    .map((price) => decimal(price.minimum_cost))
    .filter((value): value is number => value !== undefined)
  return values.length ? Math.min(...values) : undefined
}

export function modelMarketPriceCohort(
  models: OwnerModelMarketModel[],
  dimension?: OwnerModelMarketPrice['dimension'],
): ModelMarketPriceCohort | undefined {
  if (!dimension) return undefined
  const cohorts = new Map<string, ModelMarketPriceCohort>()
  for (const model of models) {
    for (const price of model.prices) {
      if (price.dimension !== dimension || price.minimum_cost === null) continue
      const cohort = { dimension, currency: price.currency, perTokens: price.per_tokens }
      cohorts.set(`${cohort.currency}\u0000${cohort.perTokens}`, cohort)
      if (cohorts.size > 1) return undefined
    }
  }
  return cohorts.values().next().value
}

function descending(left?: number, right?: number) {
  if (left === undefined && right === undefined) return 0
  if (left === undefined) return 1
  if (right === undefined) return -1
  return right - left
}

function ascending(left?: number, right?: number) {
  if (left === undefined && right === undefined) return 0
  if (left === undefined) return 1
  if (right === undefined) return -1
  return left - right
}

function statusRank(status: OwnerModelMarketStatus) {
  if (status === 'ready') return 0
  if (status === 'price_only') return 1
  return 2
}

export function modelMarketModels(
  models: OwnerModelMarketModel[],
  filter: ModelMarketViewFilter,
): OwnerModelMarketModel[] {
  const query = filter.query?.trim().toLocaleLowerCase() ?? ''
  const visible = models.filter(
    (model) =>
      (!query || model.model_key.toLocaleLowerCase().includes(query)) &&
      (!filter.status || model.status === filter.status),
  )
  return visible.sort((left, right) => {
    let comparison = 0
    switch (filter.sort) {
      case 'lowest_price':
        if (filter.priceCohort) {
          comparison = ascending(
            modelMinimumPrice(left, filter.priceCohort),
            modelMinimumPrice(right, filter.priceCohort),
          )
        }
        break
      case 'quality':
        comparison = descending(
          decimal(left.best_quality?.quality_score),
          decimal(right.best_quality?.quality_score),
        )
        break
      case 'success':
        comparison = descending(
          decimal(left.best_quality?.success_rate),
          decimal(right.best_quality?.success_rate),
        )
        break
      case 'latency':
        comparison = ascending(
          decimal(left.best_quality?.ttft_p95_ms),
          decimal(right.best_quality?.ttft_p95_ms),
        )
        break
      default:
        comparison = statusRank(left.status) - statusRank(right.status)
        if (comparison === 0) comparison = right.frontier_option_count - left.frontier_option_count
        if (comparison === 0)
          comparison = right.comparable_offer_count - left.comparable_offer_count
    }
    return comparison || left.model_key.localeCompare(right.model_key)
  })
}

export function priceKey(price: OwnerModelMarketPrice) {
  return [
    price.dimension,
    price.currency,
    price.per_tokens,
    price.minimum_cost ?? '',
    price.maximum_cost ?? '',
    price.trusted_option_count,
  ].join(':')
}
