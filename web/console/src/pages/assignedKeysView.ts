export function assignedKeyVersionLabel(version: number): string {
  return Number.isInteger(version) && version > 0 ? `v${version}` : '-'
}
