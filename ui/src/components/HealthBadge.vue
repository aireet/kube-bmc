<script setup lang="ts">
import { NTag } from 'naive-ui'
import type { Health } from '../types'
import { healthLabel, healthType } from '../format'

defineProps<{ health?: Health; size?: 'small' | 'medium' | 'large' }>()
</script>

<template>
  <n-tag :type="healthType(health)" :size="size ?? 'small'" round :bordered="false">
    <template #icon><span class="dot" :class="health ?? 'Unknown'" /></template>
    {{ healthLabel(health) }}
  </n-tag>
</template>

<style scoped>
.dot { display: inline-block; width: 7px; height: 7px; border-radius: 50%; background: currentColor; margin: 0 2px; }
.Critical { animation: pulse 1.6s ease-in-out infinite; }
@keyframes pulse { 50% { opacity: 0.35; } }
</style>
