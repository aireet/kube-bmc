import type { Config, PowerAction, PowerState, Snapshot, View } from './types'

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, { ...init, headers: { 'Content-Type': 'application/json', ...init?.headers } })
  const body = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(body.error ?? `${res.status} ${res.statusText}`)
  return body as T
}

const enc = encodeURIComponent

export const api = {
  config: () => request<Config>('/api/v1/config'),
  list: () => request<View[]>('/api/v1/bmcs'),
  get: (name: string) => request<View>(`/api/v1/bmcs/${enc(name)}`),
  live: (name: string) => request<Snapshot>(`/api/v1/bmcs/${enc(name)}/live`),
  powerState: (name: string) => request<{ powerState: PowerState }>(`/api/v1/bmcs/${enc(name)}/power`),
  power: (name: string, action: PowerAction) =>
    request<{ result: string }>(`/api/v1/bmcs/${enc(name)}/power`, {
      method: 'POST',
      body: JSON.stringify({ action, confirm: name }),
    }),
}
