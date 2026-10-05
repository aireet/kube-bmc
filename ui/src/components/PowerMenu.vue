<script setup lang="ts">
import { computed, ref } from 'vue'
import { NButton, NDropdown, NModal, NInput, NIcon, NTooltip, useMessage } from 'naive-ui'
import { PowerOutline, ChevronDownOutline } from '@vicons/ionicons5'
import { api } from '../api'
import { config } from '../store'
import { powerActions, type PowerAction, type View } from '../types'
import { t } from '../i18n'

const props = defineProps<{ v: View }>()
const emit = defineEmits<{ done: [] }>()
const message = useMessage()

const disabledReason = computed(() =>
  !config.value.powerActions ? t('powerDisabled') : !props.v.oobConfigured ? t('oobMissing') : '',
)
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
const busy = ref(false)

function pick(a: PowerAction) {
  action.value = a
  typed.value = ''
}

async function run() {
  if (!action.value) return
  busy.value = true
  try {
    await api.power(props.v.name, action.value)
    message.success(t('actionSent', { action: t(`action.${action.value}`), name: props.v.name }))
    action.value = undefined
    emit('done')
  } catch (e) {
    message.error((e as Error).message)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <n-tooltip :disabled="!disabledReason">
    <template #trigger>
      <n-dropdown :options="options" trigger="click" :disabled="!!disabledReason" @select="pick">
        <n-button :disabled="!!disabledReason" icon-placement="right">
          <template #icon><n-icon :component="ChevronDownOutline" /></template>
          <n-icon :component="PowerOutline" style="margin-right: 6px" />{{ t('powerActions') }}
        </n-button>
      </n-dropdown>
    </template>
    {{ disabledReason }}
  </n-tooltip>

  <n-modal :show="!!action" preset="dialog" type="warning" :title="t('confirmTitle')" :mask-closable="!busy"
    @update:show="(s: boolean) => !s && (action = undefined)">
    <p>{{ t('confirmBody', { action: action ? t(`action.${action}`) : '', name: v.name }) }}</p>
    <p class="muted" style="margin-bottom: 8px">{{ t('confirmType') }}: <b class="mono">{{ v.name }}</b></p>
    <n-input v-model:value="typed" :placeholder="v.name" autofocus @keyup.enter="typed === v.name && run()" />
    <template #action>
      <n-button :disabled="busy" @click="action = undefined">{{ t('cancel') }}</n-button>
      <n-button type="error" :disabled="typed !== v.name" :loading="busy" @click="run">{{ t('execute') }}</n-button>
    </template>
  </n-modal>
</template>
