export function formatFreshComparableCoverage(value: string | null): string | null {
  if (value === null) return null

  const [whole, fraction = ''] = value.split('.')
  const shifted = `${whole}${fraction.padEnd(2, '0')}`
  const splitAt = whole.length + 2
  const integer = shifted.slice(0, splitAt).replace(/^0+(?=\d)/, '')
  const remainder = shifted.slice(splitAt).replace(/0+$/, '')

  return remainder ? `${integer}.${remainder}` : integer
}
