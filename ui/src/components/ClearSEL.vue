<script setup lang="ts">
import { computed, ref } from 'vue'
import { NButton, NModal, NInput, NFormItem, NIcon, NTooltip, useMessage } from 'naive-ui'
import { TrashOutline } from '@vicons/ionicons5'
import { config } from '../store'
import type { View } from '../types'
import { runAction } from '../actions'
import { t } from '../i18n'

const props = defineProps<{ v: View }>()
const emit = defineEmits<{ done: [] }>()
const message = useMessage()

const open = ref(false)
const typed = ref('')
const reason = ref('')
const busy = ref(false)
const unavailable = computed(() => {
  if (!config.value.actions) return t('actionsDisabled')
  if (!props.v.inBandPower) return t('agentRequired')
  return ''
})
const canSubmit = computed(() => typed.value === props.v.name && reason.value.trim() !== '')

function show() {
  typed.value = ''
  reason.value = ''
  open.value = true
}

async function run() {
  if (!canSubmit.value) return
  busy.value = true
  try {
    await runAction(message, props.v.name, 'ClearSEL', reason.value.trim(), () => emit('done'))
    open.value = false
  } catch (e) {
    message.error((e as Error).message)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <n-tooltip :disabled="!unavailable">
    <template #trigger>
      <span>
        <n-button size="small" :disabled="!!unavailable" @click="show">
          <template #icon><n-icon :component="TrashOutline" /></template>{{ t('clearSEL') }}
        </n-button>
      </span>
    </template>
    {{ unavailable }}
  </n-tooltip>

  <n-modal :show="open" preset="dialog" type="warning" :title="t('clearSELTitle')" :mask-closable="!busy"
    @update:show="(s: boolean) => !s && (open = false)">
    <p>{{ t('clearSELBody', { name: v.name, entries: String(v.status.sel?.entries ?? 0) }) }}</p>
    <n-form-item :label="t('reason')" :show-feedback="false" style="margin: 12px 0">
      <n-input v-model:value="reason" :placeholder="t('reasonPlaceholder')" maxlength="512" />
    </n-form-item>
    <n-form-item :show-feedback="false">
      <template #label>{{ t('confirmType') }}: <b class="mono">{{ v.name }}</b></template>
      <n-input v-model:value="typed" :placeholder="v.name" @keyup.enter="run" />
    </n-form-item>
    <template #action>
      <n-button :disabled="busy" @click="open = false">{{ t('cancel') }}</n-button>
      <n-button type="error" :disabled="!canSubmit" :loading="busy" @click="run">{{ t('clearSEL') }}</n-button>
    </template>
  </n-modal>
</template>
