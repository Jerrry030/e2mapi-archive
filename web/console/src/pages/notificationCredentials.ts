import type { SecretRef } from '../api/types'

export interface NotificationCredentialOption {
  value: string
  label: string
}

export function isManagedPersonalNotificationRef(ref: string): boolean {
  return /\/notification\/personal-(?:feishu|qq)$/.test(ref)
}

export function notificationCredentialOptions(
  secrets: SecretRef[] | null | undefined,
  userId: number,
): NotificationCredentialOption[] {
  return (secrets ?? [])
    .filter(
      (secret) =>
        secret.user_id === userId &&
        secret.kind === 'notification' &&
        secret.exists &&
        !isManagedPersonalNotificationRef(secret.ref),
    )
    .map((secret) => ({ value: secret.ref, label: secret.name }))
}

export function safeNotificationCredentialRef(
  targetRef: string | undefined,
  userId: number,
): string | undefined {
  const prefix = `credential_ref:user/${userId}/notification/`
  if (!targetRef?.startsWith(prefix)) return undefined
  const name = targetRef.slice(prefix.length)
  return name && !name.includes('/') && !name.includes('\\') ? targetRef : undefined
}
