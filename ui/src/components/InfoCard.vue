<script setup lang="ts">
import { NCard, NIcon } from 'naive-ui'
import type { Component } from 'vue'

defineProps<{ title: string; icon: Component; rows: [string, string | number | undefined | null, boolean?][] }>()
</script>

<template>
  <n-card size="small" :bordered="false" class="info">
    <template #header>
      <span class="hdr"><n-icon :component="icon" class="ic" />{{ title }}</span>
    </template>
    <dl>
      <template v-for="[k, v, mono] in rows" :key="k">
        <dt class="muted">{{ k }}</dt>
        <dd :class="{ mono }">{{ v === '' || v == null ? '—' : v }}</dd>
      </template>
    </dl>
    <slot />
  </n-card>
</template>

<style scoped>
.info { border: 1px solid var(--kb-border); background: var(--kb-surface); }
.hdr { display: flex; align-items: center; gap: 8px; font-size: 14px; font-weight: 600; }
.ic { color: #18a058; }
dl { display: grid; grid-template-columns: minmax(110px, auto) 1fr; gap: 8px 16px; margin: 0; font-size: 13px; }
dt { white-space: nowrap; }
dd { margin: 0; word-break: break-all; }
</style>
