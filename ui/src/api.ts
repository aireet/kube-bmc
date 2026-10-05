import type { ActionType, BMCAction, Config, Me, Snapshot, View } from './types'
import { config } from './store'

export class UnauthenticatedError extends Error {}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, { ...init, headers: { 'Content-Type': 'application/json', ...init?.headers } })
  const body = await res.json().catch(() => ({}))
  if (res.status === 401 && !path.startsWith('/auth/')) {
    const rd = encodeURIComponent(location.pathname + location.search)
    if (config.value.auth === 'oidc' && !location.search.includes('signed_out')) location.assign(`/auth/login?rd=${rd}`)
    if (config.value.auth === 'password' && location.pathname !== '/login') location.assign(`/login?rd=${rd}`)
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
  login: (username: string, password: string) =>
    request<Me>('/auth/login', { method: 'POST', body: JSON.stringify({ username, password }) }),
  act: (name: string, action: ActionType, reason: string, confirm = name) =>
    request<BMCAction>(`/api/v1/bmcs/${enc(name)}/actions`, {
      method: 'POST',
      body: JSON.stringify({ action, confirm, reason }),
    }),
}
