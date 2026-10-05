<script setup lang="ts">
import { computed, ref } from 'vue'
import { NButton, NDropdown, NModal, NInput, NIcon, NPopover, NFormItem, useMessage } from 'naive-ui'
import { PowerOutline, ChevronDownOutline, LockClosedOutline } from '@vicons/ionicons5'
import { api } from '../api'
import { config } from '../store'
import { phaseDone, powerActions, type PowerAction, type View } from '../types'
import { t } from '../i18n'

const props = defineProps<{ v: View }>()
const emit = defineEmits<{ done: [] }>()
const message = useMessage()

const lockedReason = computed(() => {
  if (!config.value.powerActions) return t('powerDisabled')
  if (!props.v.oobConfigured) return t('oobMissing')
  return ''
})
const destructive: PowerAction[] = ['ForceOff', 'ForceRestart', 'PowerCycle', 'GracefulShutdown']
const options = computed(() =>
  powerActions.map((a) => ({
    key: a,
    label: t(`action.${a}`),
    props: destructive.includes(a) ? { style: 'color:#d03050' } : undefined,
  })),
)

const action = ref<PowerAction>()
const typed = ref('')
const reason = ref('')
const busy = ref(false)
const canSubmit = computed(() => typed.value === props.v.name && reason.value.trim() !== '')

function pick(a: PowerAction) {
  action.value = a
  typed.value = ''
  reason.value = ''
}

async function track(name: string, label: string) {
  for (let i = 0; i < 90; i++) {
    await new Promise((r) => setTimeout(r, 2000))
    const a = await api.action(name).catch(() => undefined)
    const phase = a?.status?.phase
    if (phaseDone(phase)) {
      const text = `${label}: ${phase} — ${a?.status?.message ?? ''}`
      if (phase === 'Succeeded') message.success(text, { duration: 6000 })
      else message.error(text, { duration: 10000 })
      emit('done')
      return
    }
  }
}

async function run() {
  if (!action.value || !canSubmit.value) return
  busy.value = true
  const label = `${t(`action.${action.value}`)} ${props.v.name}`
  try {
    const a = await api.power(props.v.name, action.value, reason.value.trim())
    message.info(t('actionCreated', { name: a.metadata.name }))
    action.value = undefined
    emit('done')
    void track(a.metadata.name, label)
  } catch (e) {
    message.error((e as Error).message)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <!-- A disabled button does not emit hover events, so the reason is shown in a popover. -->
  <n-popover v-if="lockedReason" trigger="click" placement="bottom-end" style="max-width: 340px">
    <template #trigger>
      <n-button>
        <n-icon :component="LockClosedOutline" style="margin-right: 6px" />{{ t('powerActions') }}
      </n-button>
    </template>
    <div class="why">
      <b>{{ t('powerLocked') }}</b>
      <p>{{ lockedReason }}</p>
      <a href="https://github.com/aireet/kube-bmc#power-actions" target="_blank" rel="noopener">{{ t('howToEnable') }} →</a>
    </div>
  </n-popover>
  <n-dropdown v-else :options="options" trigger="click" @select="pick">
    <n-button icon-placement="right">
      <template #icon><n-icon :component="ChevronDownOutline" /></template>
      <n-icon :component="PowerOutline" style="margin-right: 6px" />{{ t('powerActions') }}
    </n-button>
  </n-dropdown>

  <n-modal :show="!!action" preset="dialog" type="warning" :title="t('confirmTitle')" :mask-closable="!busy"
    @update:show="(s: boolean) => !s && (action = undefined)">
    <p>{{ t('confirmBody', { action: action ? t(`action.${action}`) : '', name: v.name }) }}</p>
    <n-form-item :label="t('reason')" :show-feedback="false" style="margin: 12px 0">
      <n-input v-model:value="reason" :placeholder="t('reasonPlaceholder')" maxlength="512" />
    </n-form-item>
    <n-form-item :show-feedback="false">
      <template #label>{{ t('confirmType') }}: <b class="mono">{{ v.name }}</b></template>
      <n-input v-model:value="typed" :placeholder="v.name" @keyup.enter="run" />
    </n-form-item>
    <template #action>
      <n-button :disabled="busy" @click="action = undefined">{{ t('cancel') }}</n-button>
      <n-button type="error" :disabled="!canSubmit" :loading="busy" @click="run">{{ t('execute') }}</n-button>
    </template>
  </n-modal>
</template>

<style scoped>
.why { font-size: 13px; line-height: 1.6; }
.why p { margin: 4px 0 8px; }
.why a { color: #18a058; text-decoration: none; }
</style>
