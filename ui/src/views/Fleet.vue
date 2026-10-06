<script setup lang="ts">
import { computed, h, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import {
  NInput, NSelect, NRadioGroup, NRadioButton, NDataTable, NEmpty, NAlert, NSkeleton, NIcon, NButton, NTooltip,
  type DataTableColumns,
} from 'naive-ui'
import {
  ServerOutline, CheckmarkCircleOutline, WarningOutline, CloseCircleOutline, PowerOutline, FlashOutline,
  ThermometerOutline, SearchOutline, GridOutline, ListOutline, RefreshOutline,
} from '@vicons/ionicons5'
import { api } from '../api'
import { usePoll } from '../poll'
import type { Health, View } from '../types'
import { t } from '../i18n'
import { ago, cpuShort, gpuSummary, healthColor, watts } from '../format'
import StatTile from '../components/StatTile.vue'
import ServerCard from '../components/ServerCard.vue'
import HealthBadge from '../components/HealthBadge.vue'
import PowerBadge from '../components/PowerBadge.vue'

const router = useRouter()
const { data, error, loading, updatedAt, refresh } = usePoll(api.list, 15000)
const views = computed(() => data.value ?? [])

function pref(key: string, fallback: string) {
  try {
    return localStorage.getItem(key) ?? fallback
  } catch {
    return fallback
  }
}
const query = ref('')
const healthFilter = ref<Health | 'all'>('all')
const vendor = ref<string | null>(null)
const layout = ref<'cards' | 'table'>(pref('kube-bmc.layout', 'cards') as 'cards')
watch(layout, (v) => {
  try {
    localStorage.setItem('kube-bmc.layout', v)
  } catch {
    /* storage unavailable */
  }
})

const rank: Record<string, number> = { Critical: 0, Warning: 1, Unknown: 2, OK: 3 }

const stats = computed(() => {
  const v = views.value
  const by = (h: Health) => v.filter((x) => (x.status.health ?? 'Unknown') === h).length
  const gpuCount = v.reduce((a, x) => a + (x.status.hardware?.gpus?.reduce((n, g) => n + g.count, 0) ?? x.node?.gpus ?? 0), 0)
  const watt = v.reduce((a, x) => a + (x.status.powerState === 'On' ? x.status.powerWatts ?? 0 : 0), 0)
  const inlets = v.map((x) => x.status.inletTemperature).filter((x): x is number => x != null)
  return {
    total: v.length,
    ok: by('OK'),
    warning: by('Warning'),
    critical: by('Critical'),
    on: v.filter((x) => x.status.powerState === 'On').length,
    watts: watt,
    inlet: inlets.length ? Math.round(inlets.reduce((a, b) => a + b, 0) / inlets.length) : undefined,
    gpus: gpuCount,
  }
})

const vendors = computed(() =>
  [...new Set(views.value.map((v) => v.status.device?.manufacturer).filter(Boolean) as string[])]
    .sort()
    .map((x) => ({ label: x, value: x })),
)

const filtered = computed(() => {
  const q = query.value.trim().toLowerCase()
  return views.value
    .filter((v) => healthFilter.value === 'all' || (v.status.health ?? 'Unknown') === healthFilter.value)
    .filter((v) => !vendor.value || v.status.device?.manufacturer === vendor.value)
    .filter((v) => {
      if (!q) return true
      const s = v.status
      return [v.name, s.network?.ipAddress, s.network?.macAddress, s.device?.serialNumber, s.device?.product,
        s.device?.manufacturer, v.node?.internalIP, v.node?.gpuModel]
        .some((x) => x?.toLowerCase().includes(q))
    })
    .sort((a, b) => rank[a.status.health ?? 'Unknown'] - rank[b.status.health ?? 'Unknown'] || a.name.localeCompare(b.name))
})

function toggleHealth(h: Health) {
  healthFilter.value = healthFilter.value === h ? 'all' : h
}

const columns = computed<DataTableColumns<View>>(() => [
  {
    title: t('name'), key: 'name', fixed: 'left', width: 170, sorter: (a, b) => a.name.localeCompare(b.name),
    render: (v) => h('span', { style: 'font-weight:600' }, v.name),
  },
  {
    title: t('health'), key: 'health', width: 110,
    sorter: (a, b) => rank[a.status.health ?? 'Unknown'] - rank[b.status.health ?? 'Unknown'],
    render: (v) => h(HealthBadge, { health: v.status.health }),
  },
  { title: t('power'), key: 'power', width: 90, render: (v) => h(PowerBadge, { state: v.status.powerState }) },
  {
    title: 'W', key: 'watts', width: 90, align: 'right',
    sorter: (a, b) => (a.status.powerWatts ?? -1) - (b.status.powerWatts ?? -1),
    render: (v) => h('span', { class: 'tnum' }, watts(v.status.powerWatts)),
  },
  {
    title: t('inlet'), key: 'inlet', width: 80, align: 'right',
    sorter: (a, b) => (a.status.inletTemperature ?? -99) - (b.status.inletTemperature ?? -99),
    render: (v) => h('span', { class: 'tnum' }, v.status.inletTemperature != null ? `${v.status.inletTemperature}°C` : '—'),
  },
  {
    title: t('model'), key: 'model', minWidth: 220, ellipsis: { tooltip: true },
    render: (v) => `${v.status.device?.manufacturer ?? '—'} ${v.status.device?.product ?? ''}`,
  },
  {
    title: t('gpus'), key: 'gpus', width: 150,
    render: (v) => gpuSummary(v.status.hardware, v.node?.gpus, v.node?.gpuModel) || '—',
  },
  {
    title: t('cpu'), key: 'cpu', width: 230, ellipsis: { tooltip: true },
    render: (v) => {
      const c = v.status.hardware?.cpu
      return c?.model ? `${c.sockets ?? 1}× ${cpuShort(c.model)}` : '—'
    },
  },
  { title: t('bmcIP'), key: 'ip', width: 130, render: (v) => h('span', { class: 'mono' }, v.status.network?.ipAddress ?? '—') },
  { title: t('firmware'), key: 'fw', width: 120, ellipsis: { tooltip: true }, render: (v) => v.status.controller?.firmwareVersion ?? '—' },
  { title: t('serial'), key: 'serial', width: 170, ellipsis: { tooltip: true }, render: (v) => h('span', { class: 'mono' }, v.status.device?.serialNumber ?? '—') },
  {
    title: t('issues'), key: 'issues', minWidth: 260, ellipsis: { tooltip: true },
    render: (v) => {
      const p = v.status.problems?.[0]
      if (!p) return h('span', { class: 'muted' }, '—')
      const n = v.status.problems!.length
      return h('span', { style: `color:${healthColor[p.severity]}` }, `${p.source}: ${p.message}${n > 1 ? `  (+${n - 1})` : ''}`)
    },
  },
])

const rowProps = (v: View) => ({ style: 'cursor:pointer', onClick: () => router.push({ name: 'server', params: { name: v.name } }) })
</script>

<template>
  <main class="page">
    <section class="stats">
      <StatTile :icon="ServerOutline" :label="t('servers')" :value="stats.total" :sub="stats.gpus ? `${stats.gpus} ${t('gpus')}` : undefined"
        :active="healthFilter === 'all'" @click="healthFilter = 'all'" />
      <StatTile :icon="CheckmarkCircleOutline" :label="t('healthy')" :value="stats.ok" :color="healthColor.OK"
        :active="healthFilter === 'OK'" @click="toggleHealth('OK')" />
      <StatTile :icon="WarningOutline" :label="t('warning')" :value="stats.warning" :color="healthColor.Warning"
        :active="healthFilter === 'Warning'" @click="toggleHealth('Warning')" />
      <StatTile :icon="CloseCircleOutline" :label="t('critical')" :value="stats.critical" :color="healthColor.Critical"
        :active="healthFilter === 'Critical'" @click="toggleHealth('Critical')" />
      <StatTile :icon="PowerOutline" :label="t('poweredOn')" :value="`${stats.on}/${stats.total}`" color="#2080f0" />
      <StatTile :icon="FlashOutline" :label="t('powerDraw')" :value="watts(stats.watts)" color="#8a5cf6" />
      <StatTile :icon="ThermometerOutline" :label="t('avgInlet')" :value="stats.inlet != null ? `${stats.inlet}°C` : '—'" color="#0ea5e9" />
    </section>

    <div v-if="stats.total" class="healthbar" aria-hidden="true">
      <span :style="{ flex: stats.critical, background: healthColor.Critical }" />
      <span :style="{ flex: stats.warning, background: healthColor.Warning }" />
      <span :style="{ flex: stats.ok, background: healthColor.OK }" />
      <span :style="{ flex: stats.total - stats.ok - stats.warning - stats.critical, background: healthColor.Unknown }" />
    </div>

    <section class="toolbar">
      <n-input v-model:value="query" :placeholder="t('search')" clearable class="search">
        <template #prefix><n-icon :component="SearchOutline" /></template>
      </n-input>
      <n-select v-model:value="vendor" :options="vendors" :placeholder="t('allVendors')" clearable class="vendor" />
      <div class="spacer" />
      <n-tooltip v-if="updatedAt">
        <template #trigger>
          <n-button quaternary size="small" @click="refresh">
            <template #icon><n-icon :component="RefreshOutline" /></template>
            <span class="muted">{{ t('updated') }} {{ ago(updatedAt) }} {{ t('ago') }}</span>
          </n-button>
        </template>
        {{ t('refresh') }}
      </n-tooltip>
      <n-radio-group v-model:value="layout" size="small">
        <n-radio-button value="cards"><n-icon :component="GridOutline" /> {{ t('cards') }}</n-radio-button>
        <n-radio-button value="table"><n-icon :component="ListOutline" /> {{ t('table') }}</n-radio-button>
      </n-radio-group>
    </section>

    <n-alert v-if="error" type="error" :bordered="false" class="err">{{ error }}</n-alert>

    <div v-if="loading" class="grid">
      <n-skeleton v-for="i in 8" :key="i" height="168px" :sharp="false" style="border-radius: 14px" />
    </div>
    <n-empty v-else-if="!views.length" :description="t('noServers')" class="empty">
      <template #extra><p class="muted hint">{{ t('noServersHint') }}</p></template>
    </n-empty>
    <n-empty v-else-if="!filtered.length" :description="t('noMatch')" class="empty" />
    <div v-else-if="layout === 'cards'" class="grid">
      <ServerCard v-for="v in filtered" :key="v.name" :v="v" />
    </div>
    <n-data-table v-else :columns="columns" :data="filtered" :row-props="rowProps" :row-key="(v: View) => v.name"
      :scroll-x="1600" size="small" :bordered="false" class="table" />
  </main>
</template>

<style scoped>
.stats {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(170px, 1fr));
  gap: 12px;
}
.stats > :nth-child(-n + 4) { cursor: pointer; }
.healthbar {
  display: flex;
  height: 6px;
  border-radius: 99px;
  overflow: hidden;
  margin: 16px 0 4px;
  gap: 2px;
}
.healthbar span { min-width: 0; transition: flex 0.4s ease; }
.toolbar {
  display: flex;
  align-items: center;
  gap: 10px;
  margin: 18px 0 16px;
  flex-wrap: wrap;
}
.search { max-width: 360px; }
.vendor { width: 190px; }
.spacer { flex: 1; }
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(310px, 1fr));
  gap: 14px;
}
.table { background: var(--kb-surface); border-radius: 12px; border: 1px solid var(--kb-border); overflow: hidden; }
.empty { margin-top: 80px; }
.hint { max-width: 520px; text-align: center; line-height: 1.6; }
.err { margin-bottom: 14px; }
@media (max-width: 640px) {
  .search, .vendor { max-width: none; width: 100%; }
}
</style>
