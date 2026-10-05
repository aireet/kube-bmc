import type { BMCAction, Config, Me, PowerAction, Snapshot, View } from './types'
import { config } from './store'

export class UnauthenticatedError extends Error {}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, { ...init, headers: { 'Content-Type': 'application/json', ...init?.headers } })
  const body = await res.json().catch(() => ({}))
  if (res.status === 401) {
    if (config.value.login && !location.search.includes('signed_out')) {
      location.assign(`/auth/login?rd=${encodeURIComponent(location.pathname + location.search)}`)
    }
    throw new UnauthenticatedError(body.error ?? 'authentication required')
  }
  if (!res.ok) throw new Error(body.error ?? `${res.status} ${res.statusText}`)
  return body as T
}

const enc = encodeURIComponent

export const api = {
  config: () => request<Config>('/api/v1/config'),
  me: () => request<Me>('/api/v1/me'),
  list: () => request<View[]>('/api/v1/bmcs'),
  get: (name: string) => request<View>(`/api/v1/bmcs/${enc(name)}`),
  live: (name: string) => request<Snapshot>(`/api/v1/bmcs/${enc(name)}/live`),
  actions: (bmc?: string) => request<BMCAction[]>(`/api/v1/actions${bmc ? `?bmc=${enc(bmc)}` : ''}`),
  action: (name: string) => request<BMCAction>(`/api/v1/actions/${enc(name)}`),
  power: (name: string, action: PowerAction, reason: string) =>
    request<BMCAction>(`/api/v1/bmcs/${enc(name)}/power`, {
      method: 'POST',
      body: JSON.stringify({ action, confirm: name, reason }),
    }),
}
