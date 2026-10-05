<script setup lang="ts">
import { computed, ref } from 'vue'
import { NInput, NSwitch, NIcon, NTooltip, NEmpty } from 'naive-ui'
import { WarningOutline, ThermometerOutline, SpeedometerOutline, FlashOutline, BatteryChargingOutline, PulseOutline, ToggleOutline, AnalyticsOutline, SearchOutline } from '@vicons/ionicons5'
import type { Sensor } from '../types'
import { num, unit } from '../format'
import { t } from '../i18n'

const props = defineProps<{ sensors: Sensor[] }>()
const query = ref('')
const showNoReading = ref(false)

const groups = [
  { type: 'temperature', icon: ThermometerOutline },
  { type: 'fan', icon: SpeedometerOutline },
  { type: 'power', icon: FlashOutline },
  { type: 'voltage', icon: BatteryChargingOutline },
  { type: 'current', icon: PulseOutline },
  { type: 'utilization', icon: AnalyticsOutline },
  { type: 'discrete', icon: ToggleOutline },
]
const sevRank = { critical: 0, warning: 1, ok: 2, noreading: 3 }

const grouped = computed(() => {
  const q = query.value.trim().toLowerCase()
  const list = props.sensors.filter(
    (s) => (showNoReading.value || s.severity !== 'noreading') && (!q || s.name.toLowerCase().includes(q) || s.reading.toLowerCase().includes(q)),
  )
  const sort = (a: Sensor, b: Sensor) =>
    sevRank[a.severity] - sevRank[b.severity] || a.name.localeCompare(b.name, undefined, { numeric: true })
  const attention = { type: 'attention', icon: WarningOutline, sensors: list.filter((s) => s.severity === 'warning' || s.severity === 'critical').sort(sort) }
  return [attention, ...groups.map((g) => ({ ...g, sensors: list.filter((s) => s.type === g.type).sort(sort) }))]
    .filter((g) => g.sensors.length)
})

/** Position of the reading on a 0–100% bar spanning the sensor's threshold range. */
function bar(s: Sensor): number | undefined {
  if (s.value == null) return undefined
  const th = s.thresholds
  const hi = th?.ucr ?? th?.unr ?? th?.unc
  if (s.type === 'fan') {
    const max = Math.max(...props.sensors.filter((x) => x.type === 'fan' && x.value != null).map((x) => x.value!), 1)
    return Math.min(100, (s.value / max) * 100)
  }
  if (hi == null || hi <= 0) return undefined
  const lo = s.type === 'voltage' ? th?.lcr ?? th?.lnr ?? 0 : 0
  return Math.max(2, Math.min(100, ((s.value - lo) / (hi - lo)) * 100))
}

function thresholdText(s: Sensor): string {
  const th = s.thresholds
  if (!th) return ''
  const u = unit(s.unit)
  return (['lnr', 'lcr', 'lnc', 'unc', 'ucr', 'unr'] as const)
    .filter((k) => th[k] != null)
    .map((k) => `${k.toUpperCase()} ${num(th[k]!)}${u}`)
    .join(' · ')
}
</script>

<template>
  <div class="toolbar">
    <n-input v-model:value="query" :placeholder="t('filterSensors')" clearable size="small" class="q">
      <template #prefix><n-icon :component="SearchOutline" /></template>
    </n-input>
    <label class="toggle muted"><n-switch v-model:value="showNoReading" size="small" /> {{ t('showNoReading') }}</label>
  </div>
  <n-empty v-if="!grouped.length" :description="t('noMatch')" style="margin: 48px 0" />
  <div class="groups">
    <section v-for="g in grouped" :key="g.type" class="group" :class="{ attention: g.type === 'attention' }">
      <header>
        <n-icon :component="g.icon" class="gic" />
        <span>{{ t(g.type) }}</span>
        <span class="count muted">{{ g.sensors.length }}</span>
      </header>
      <div v-for="s in g.sensors" :key="s.name" class="row" :class="s.severity">
        <span class="sev" />
        <span class="name" :title="s.name">{{ s.name }}</span>
        <n-tooltip :disabled="!s.thresholds">
          <template #trigger>
            <span class="track" :class="{ empty: bar(s) == null }"><span v-if="bar(s) != null" class="fill" :style="{ width: `${bar(s)}%` }" /></span>
          </template>
          <span class="mono">{{ thresholdText(s) }}</span>
        </n-tooltip>
        <span class="val tnum">
          <template v-if="s.value != null">{{ num(s.value) }}<small> {{ unit(s.unit) }}</small></template>
          <template v-else-if="s.severity === 'noreading'"><span class="muted">{{ t('noReading') }}</span></template>
          <template v-else>{{ s.reading || '—' }}</template>
        </span>
      </div>
    </section>
  </div>
</template>

<style scoped>
.toolbar { display: flex; gap: 16px; align-items: center; margin-bottom: 14px; flex-wrap: wrap; }
.q { max-width: 280px; }
.toggle { display: flex; align-items: center; gap: 8px; font-size: 13px; cursor: pointer; }
.groups { columns: 2 460px; column-gap: 14px; }
.group {
  break-inside: avoid;
  margin-bottom: 14px;
  padding: 12px 14px 8px;
  border-radius: 12px;
  background: var(--kb-surface);
  border: 1px solid var(--kb-border);
}
header { display: flex; align-items: center; gap: 8px; font-weight: 600; font-size: 13.5px; margin-bottom: 6px; }
.gic { color: #18a058; }
.attention { border-color: color-mix(in srgb, #d03050 35%, var(--kb-border)); }
.attention .gic { color: #d03050; }
.count { font-weight: 400; font-size: 12px; }
.row {
  display: grid;
  grid-template-columns: 8px minmax(120px, 1.3fr) minmax(60px, 1fr) minmax(86px, auto);
  align-items: center;
  gap: 10px;
  padding: 5px 0;
  font-size: 13px;
  border-top: 1px dashed var(--kb-border);
}
.sev { width: 7px; height: 7px; border-radius: 50%; background: #18a058; }
.warning .sev { background: #f0a020; }
.critical .sev { background: #d03050; animation: pulse 1.6s ease-in-out infinite; }
.noreading .sev { background: #909399; opacity: 0.5; }
.name { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.track { height: 5px; border-radius: 99px; background: color-mix(in srgb, var(--kb-muted) 18%, transparent); overflow: hidden; }
.track.empty { visibility: hidden; }
.fill { display: block; height: 100%; border-radius: 99px; background: #18a058; transition: width 0.6s ease; }
.warning .fill { background: #f0a020; }
.critical .fill { background: #d03050; }
.val { text-align: right; white-space: nowrap; }
.val small { color: var(--kb-muted); font-size: 11px; }
.warning .val { color: #c27c0e; font-weight: 600; }
.critical .val { color: #d03050; font-weight: 600; }
@keyframes pulse { 50% { opacity: 0.35; } }
</style>
