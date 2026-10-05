<script setup lang="ts">
import { computed, ref } from 'vue'
import { NButton, NDropdown, NIcon, NPopover, useMessage } from 'naive-ui'
import { BulbOutline, ChevronDownOutline } from '@vicons/ionicons5'
import { config } from '../store'
import type { View } from '../types'
import { runAction } from '../actions'
import { t } from '../i18n'

const props = defineProps<{ v: View }>()
const emit = defineEmits<{ done: [] }>()
const message = useMessage()
const busy = ref(false)

const unavailable = computed(() => {
  if (!config.value.actions) return t('actionsDisabled')
  if (!props.v.inBandPower) return t('agentRequired')
  return ''
})
const options = computed(() => [
  { key: 'IdentifyOn', label: t('action.IdentifyOn') },
  { key: 'IdentifyOff', label: t('action.IdentifyOff') },
])

async function pick(key: 'IdentifyOn' | 'IdentifyOff') {
  busy.value = true
  try {
    await runAction(message, props.v.name, key, '', () => emit('done'))
  } catch (e) {
    message.error((e as Error).message)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <n-popover v-if="unavailable" trigger="click" placement="bottom-end" style="max-width: 320px">
    <template #trigger>
      <n-button secondary>
        <template #icon><n-icon :component="BulbOutline" /></template>{{ t('locate') }}
      </n-button>
    </template>
    {{ unavailable }}
  </n-popover>
  <n-dropdown v-else :options="options" trigger="click" @select="pick">
    <n-button secondary icon-placement="right" :loading="busy">
      <template #icon><n-icon :component="ChevronDownOutline" /></template>
      <n-icon :component="BulbOutline" style="margin-right: 6px" />{{ t('locate') }}
    </n-button>
  </n-dropdown>
</template>
