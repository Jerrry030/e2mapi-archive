import { describe, expect, it } from 'vitest'
import type { OwnerModelMarketModel } from '../api/ownerModelMarket'
import {
  modelMarketModels,
  modelMarketPriceCohort,
  modelMinimumPrice,
  priceKey,
} from './modelMarketView'

function model(
  modelKey: string,
  status: OwnerModelMarketModel['status'],
  price?: string,
  quality?: string,
  success?: string,
  latency?: string,
): OwnerModelMarketModel {
  return {
    model_key: modelKey,
    status,
    prices: price
      ? [
          {
            dimension: 'input',
            currency: 'USD',
            per_tokens: 1_000_000,
            minimum_cost: price,
            maximum_cost: price,
            trusted_option_count: 1,
          },
        ]
      : [],
    observed_offer_count: 1,
    comparable_offer_count: price ? 1 : 0,
    quality_option_count: quality ? 1 : 0,
    frontier_option_count: quality ? 1 : 0,
    freshest_evidence: 'current',
    best_quality: quality
      ? {
          quality_score: quality,
          success_rate: success ?? null,
          ttft_p95_ms: latency ?? null,
          duration_p95_ms: '1000',
          sample_count: 10,
          health_state: 'healthy',
          freshness: 'current',
          on_frontier: true,
          effective_cost: price ?? null,
          currency: 'USD',
          per_tokens: 1_000_000,
          dimension: 'input',
        }
      : null,
  }
}

describe('model market view', () => {
  const models = [
    model('gpt-premium', 'ready', '4', '95', '0.99', '800'),
    model('gpt-economy', 'ready', '1', '82', '0.95', '300'),
    model('claude-price-only', 'price_only', '2'),
    model('unknown', 'insufficient_evidence'),
  ]

  it('keeps unknown price absent instead of treating it as zero', () => {
    expect(modelMinimumPrice(models[3])).toBeUndefined()
    const priceCohort = modelMarketPriceCohort(models, 'input')
    expect(
      modelMarketModels(models, { sort: 'lowest_price', priceCohort }).map(
        (item) => item.model_key,
      ),
    ).toEqual(['gpt-economy', 'claude-price-only', 'gpt-premium', 'unknown'])
  })

  it('supports case-insensitive search, status filtering, and evidence-backed sorts', () => {
    expect(
      modelMarketModels(models, { query: 'GPT', status: 'ready', sort: 'quality' }).map(
        (item) => item.model_key,
      ),
    ).toEqual(['gpt-premium', 'gpt-economy'])
    expect(modelMarketModels(models, { sort: 'latency' }).map((item) => item.model_key)).toEqual([
      'gpt-economy',
      'gpt-premium',
      'claude-price-only',
      'unknown',
    ])
  })

  it('does not claim a lowest-price ordering across incomparable billing dimensions', () => {
    expect(
      modelMarketModels(models, { sort: 'lowest_price' }).map((item) => item.model_key),
    ).toEqual(['claude-price-only', 'gpt-economy', 'gpt-premium', 'unknown'])
  })

  it('disables price ranking when currency or token units differ', () => {
    const mixedCurrency = structuredClone(models)
    mixedCurrency[0].prices[0].currency = 'CNY'
    expect(modelMarketPriceCohort(mixedCurrency, 'input')).toBeUndefined()

    const mixedUnit = structuredClone(models)
    mixedUnit[0].prices[0].per_tokens = 1_000
    expect(modelMarketPriceCohort(mixedUnit, 'input')).toBeUndefined()
  })

  it('uses every displayed aggregate field in the React price key', () => {
    const price = models[0].prices[0]
    expect(priceKey(price)).not.toBe(priceKey({ ...price, maximum_cost: '9' }))
  })
})
