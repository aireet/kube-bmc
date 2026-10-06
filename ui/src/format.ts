import type { Health } from './types'
import { t } from './i18n'

export const healthColor: Record<Health, string> = {
  OK: '#18a058',
  Warning: '#f0a020',
  Critical: '#d03050',
  Unknown: '#909399',
}

export const healthType = (h?: Health) =>
  (({ OK: 'success', Warning: 'warning', Critical: 'error' }) as const)[h as 'OK'] ?? 'default'

export const healthLabel = (h?: Health) =>
  ({ OK: t('healthy'), Warning: t('warning'), Critical: t('critical') })[h as 'OK'] ?? t('unknown')

export function watts(w?: number): string {
  if (w == null) return '—'
  return w >= 1000 ? `${(w / 1000).toFixed(w >= 10000 ? 0 : 1)} kW` : `${w} W`
}

export function ago(iso?: string | Date): string {
  if (!iso) return '—'
  const s = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 1000))
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m`
  if (s < 86400) return `${Math.floor(s / 3600)}h`
  return `${Math.floor(s / 86400)}d`
}

const units: Record<string, string> = { 'degrees C': '°C', 'degrees F': '°F', Volts: 'V', Watts: 'W', Amps: 'A', RPM: 'RPM', percent: '%' }
export const unit = (u?: string) => (u ? units[u] ?? u : '')

export function num(v: number): string {
  if (Number.isInteger(v)) return String(v)
  return Math.abs(v) < 10 ? v.toFixed(2) : v.toFixed(1)
}

/** Shortens a CPU model, e.g. "Intel Xeon Platinum 8468V" becomes "Xeon Platinum 8468V". */
export const cpuShort = (model?: string) => (model ?? '').replace(/^(Intel|AMD)\s+/, '')

/** GPU summary of a server, preferring the hardware inventory over the node label. */
export function gpuSummary(hw?: { gpus?: { model: string; count: number }[] }, nodeGPUs?: number, nodeModel?: string): string {
  if (hw?.gpus?.length) return hw.gpus.map((g) => `${g.count}× ${gpuShort(g.model)}`).join(', ')
  if (nodeGPUs) return `${nodeGPUs}× ${gpuShort(nodeModel) || 'GPU'}`
  return ''
}

export const bmcURL = (ip?: string) => (ip ? `https://${ip}` : undefined)

/**
 * Shortens a GPU name: the node label NVIDIA-GeForce-RTX-5090 and the PCI name
 * "NVIDIA GB202 [GeForce RTX 5090]" both become RTX 5090.
 */
export function gpuShort(model?: string): string {
  if (!model) return ''
  const bracket = model.match(/\[([^\]]+)\]$/)
  if (bracket) return bracket[1]!.replace(/^GeForce /, '')
  return model
    .replace(/^NVIDIA-/, '')
    .replace(/^GeForce-/, '')
    .replace(/-SXM\d?.*$|-PCIe.*$/, '')
    .replace(/-(\d+GB).*$/, ' $1')
    .replace(/-/g, ' ')
}
