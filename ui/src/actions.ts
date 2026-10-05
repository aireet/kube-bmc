import type { MessageApi } from 'naive-ui'
import { api } from './api'
import { phaseDone, type ActionType } from './types'
import { t } from './i18n'

/**
 * Creates a BMCAction and resolves once it is created; errors from the request are thrown.
 * The outcome is reported in the background when the action finishes.
 */
export async function runAction(message: MessageApi, bmc: string, action: ActionType, reason: string, onChange: () => void) {
  const a = await api.act(bmc, action, reason)
  message.info(t('actionCreated', { name: a.metadata.name }))
  onChange()
  void track(message, a.metadata.name, `${t(`action.${action}`)} · ${bmc}`, onChange)
}

async function track(message: MessageApi, name: string, label: string, onChange: () => void) {
  for (let i = 0; i < 150; i++) {
    await new Promise((r) => setTimeout(r, 2000))
    const cur = await api.action(name).catch(() => undefined)
    const phase = cur?.status?.phase
    if (phaseDone(phase)) {
      const text = `${label}: ${cur?.status?.message ?? phase}`
      if (phase === 'Succeeded') message.success(text, { duration: 8000 })
      else message.error(text, { duration: 12000 })
      onChange()
      return
    }
  }
}
