<script setup lang="ts">
import { NTag, NIcon } from 'naive-ui'
import { PowerOutline } from '@vicons/ionicons5'
import type { PowerState } from '../types'
import { t } from '../i18n'

defineProps<{ state?: PowerState }>()
</script>

<template>
  <n-tag size="small" round :bordered="false" :type="state === 'On' ? 'success' : state === 'Off' ? 'default' : 'warning'">
    <template #icon><n-icon :component="PowerOutline" /></template>
    {{ state === 'On' ? t('on') : state === 'Off' ? t('off') : t('unknown') }}
  </n-tag>
</template>
