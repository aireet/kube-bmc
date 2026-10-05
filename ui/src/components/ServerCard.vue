<script setup lang="ts">
import { computed } from 'vue'
import { NIcon, NTooltip, NTag } from 'naive-ui'
import { FlashOutline, ThermometerOutline, HardwareChipOutline, OpenOutline, AlertCircleOutline, TimeOutline } from '@vicons/ionicons5'
import type { View } from '../types'
import HealthBadge from './HealthBadge.vue'
import PowerBadge from './PowerBadge.vue'
import { bmcURL, gpuShort, healthColor, watts } from '../format'
import { t } from '../i18n'

const props = defineProps<{ v: View }>()
const s = computed(() => props.v.status)
const health = computed(() => s.value.health ?? 'Unknown')
const top = computed(() => s.value.problems?.[0])
const more = computed(() => Math.max(0, (s.value.problems?.length ?? 0) - 1))
</script>

<template>
  <router-link :to="{ name: 'server', params: { name: v.name } }" class="card" :style="{ '--accent': healthColor[health] }">
    <div class="head">
      <div class="title">
        <span class="name">{{ v.name }}</span>
        <span class="model muted">{{ s.device?.manufacturer || '—' }} · {{ s.device?.product || '—' }}</span>
      </div>
      <HealthBadge :health="health" />
    </div>

    <div class="metrics tnum">
      <div class="metric">
        <n-icon :component="FlashOutline" class="ic" />
        <span>{{ watts(s.powerWatts) }}</span>
      </div>
      <div class="metric">
        <n-icon :component="ThermometerOutline" class="ic" />
        <span>{{ s.inletTemperature != null ? `${s.inletTemperature}°C` : '—' }}</span>
      </div>
      <div v-if="v.node?.gpus" class="metric">
        <n-icon :component="HardwareChipOutline" class="ic" />
        <span>{{ v.node.gpus }}× {{ gpuShort(v.node.gpuModel) || 'GPU' }}</span>
      </div>
    </div>

    <div class="foot">
      <a v-if="s.network?.ipAddress" class="bmc mono" :href="bmcURL(s.network.ipAddress)" target="_blank" rel="noopener" @click.stop>
        {{ s.network.ipAddress }} <n-icon :component="OpenOutline" size="12" />
      </a>
      <span v-else class="muted mono">—</span>
      <div class="badges">
        <n-tooltip v-if="v.stale">
          <template #trigger>
            <n-tag size="small" round :bordered="false" type="warning">
              <template #icon><n-icon :component="TimeOutline" /></template>{{ t('stale') }}
            </n-tag>
          </template>
          {{ t('staleHint') }}
        </n-tooltip>
        <PowerBadge :state="s.powerState" />
      </div>
    </div>

    <div v-if="top" class="problem" :class="top.severity">
      <n-icon :component="AlertCircleOutline" />
      <span class="ptext"><b>{{ top.source }}</b> {{ top.message }}</span>
      <span v-if="more" class="more">+{{ more }}</span>
    </div>
  </router-link>
</template>

<style scoped>
.card {
  position: relative;
  display: flex;
  flex-direction: column;
  gap: 14px;
  padding: 16px 18px 16px 20px;
  border-radius: 14px;
  background: var(--kb-surface);
  border: 1px solid var(--kb-border);
  text-decoration: none;
  color: inherit;
  overflow: hidden;
  transition: transform 0.15s ease, box-shadow 0.15s ease, border-color 0.15s ease;
}
.card::before {
  content: '';
  position: absolute;
  inset: 0 auto 0 0;
  width: 4px;
  background: var(--accent);
}
.card:hover {
  transform: translateY(-2px);
  border-color: color-mix(in srgb, var(--accent) 45%, var(--kb-border));
  box-shadow: 0 8px 24px -12px color-mix(in srgb, var(--accent) 45%, transparent);
}
.head { display: flex; justify-content: space-between; align-items: flex-start; gap: 8px; }
.title { display: flex; flex-direction: column; min-width: 0; }
.name { font-weight: 650; font-size: 15px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.model { font-size: 12.5px; margin-top: 2px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.metrics { display: flex; gap: 16px; flex-wrap: wrap; font-size: 13.5px; }
.metric { display: flex; align-items: center; gap: 5px; }
.ic { color: var(--kb-muted); }
.foot { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.bmc { display: inline-flex; align-items: center; gap: 4px; font-size: 12.5px; color: var(--kb-muted); text-decoration: none; }
.bmc:hover { color: #18a058; }
.badges { display: flex; gap: 6px; }
.problem {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12.5px;
  padding: 7px 10px;
  border-radius: 8px;
  margin-top: -2px;
}
.problem.Critical { background: color-mix(in srgb, #d03050 11%, transparent); color: #d03050; }
.problem.Warning { background: color-mix(in srgb, #f0a020 13%, transparent); color: #c27c0e; }
:root[data-theme='dark'] .problem.Warning { color: #f2b24c; }
:root[data-theme='dark'] .problem.Critical { color: #e86f87; }
.ptext { flex: 1; min-width: 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.more { font-weight: 600; opacity: 0.8; }
</style>
