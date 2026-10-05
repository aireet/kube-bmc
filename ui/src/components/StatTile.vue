<script setup lang="ts">
import { NIcon } from 'naive-ui'
import type { Component } from 'vue'

defineProps<{ label: string; value: string | number; sub?: string; icon: Component; color?: string; active?: boolean }>()
</script>

<template>
  <div class="tile" :class="{ active }" :style="{ '--c': color ?? '#18a058' }">
    <div class="icon"><n-icon :component="icon" size="18" /></div>
    <div class="body">
      <div class="value tnum">{{ value }}</div>
      <div class="label muted">{{ label }}<span v-if="sub" class="sub"> · {{ sub }}</span></div>
    </div>
  </div>
</template>

<style scoped>
.tile {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 14px 16px;
  border-radius: 14px;
  background: var(--kb-surface);
  border: 1px solid var(--kb-border);
  transition: border-color 0.15s ease, background 0.15s ease;
}
.tile.active { border-color: var(--c); background: color-mix(in srgb, var(--c) 7%, var(--kb-surface)); }
.icon {
  width: 36px;
  height: 36px;
  border-radius: 10px;
  display: grid;
  place-items: center;
  color: var(--c);
  background: color-mix(in srgb, var(--c) 13%, transparent);
  flex-shrink: 0;
}
.value { font-size: 22px; font-weight: 680; line-height: 1.1; letter-spacing: -0.02em; }
.label { font-size: 12.5px; margin-top: 3px; white-space: nowrap; }
</style>
