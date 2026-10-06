<script setup lang="ts">
import { computed } from 'vue'
import { NCard, NIcon, NTag } from 'naive-ui'
import { GitNetworkOutline } from '@vicons/ionicons5'
import type { Hardware } from '../types'
import { t } from '../i18n'

const props = defineProps<{ slots: NonNullable<Hardware['pcieSlots']> }>()
const used = computed(() => props.slots.filter((s) => s.inUse).length)
</script>

<template>
  <n-card size="small" :bordered="false" class="card">
    <template #header>
      <span class="hdr"><n-icon :component="GitNetworkOutline" class="ic" />{{ t('pcieSlots') }}
        <span class="muted cnt">{{ t('slotsUsed', { used: String(used), total: String(slots.length) }) }}</span></span>
    </template>
    <div class="usage">
      <span v-for="s in slots" :key="s.name" class="cell" :class="{ on: s.inUse }" :title="`${s.name}: ${s.device ?? (s.inUse ? t('inUse') : t('free'))}`" />
    </div>
    <div class="grid">
      <div v-for="s in slots" :key="s.name" class="slot" :class="{ on: s.inUse }">
        <div class="top">
          <b class="mono">{{ s.name }}</b>
          <span class="muted spec">{{ [s.generation, s.width].filter(Boolean).join(' ') }}</span>
          <n-tag size="tiny" round :bordered="false" :type="s.inUse ? 'success' : 'default'">{{ s.inUse ? t('inUse') : t('free') }}</n-tag>
        </div>
        <div class="dev" :title="s.device">{{ s.device || (s.inUse ? t('unknownDevice') : '—') }}</div>
      </div>
    </div>
  </n-card>
</template>

<style scoped>
.card { border: 1px solid var(--kb-border); background: var(--kb-surface); margin-top: 14px; }
.hdr { display: flex; align-items: center; gap: 8px; font-size: 14px; font-weight: 600; }
.ic { color: #18a058; }
.cnt { font-weight: 400; }
.usage { display: flex; gap: 4px; margin-bottom: 14px; }
.cell { flex: 1; height: 8px; border-radius: 3px; background: color-mix(in srgb, var(--kb-muted) 22%, transparent); }
.cell.on { background: #18a058; }
.grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(230px, 1fr)); gap: 10px; }
.slot { border: 1px dashed var(--kb-border); border-radius: 10px; padding: 10px 12px; }
.slot.on { border-style: solid; border-color: color-mix(in srgb, #18a058 35%, var(--kb-border)); background: color-mix(in srgb, #18a058 5%, transparent); }
.top { display: flex; align-items: center; gap: 8px; font-size: 13px; }
.spec { font-size: 12px; flex: 1; }
.dev { font-size: 12.5px; margin-top: 6px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
</style>
