<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { NButton, NIcon, NTabs, NTabPane, NAlert, NSkeleton, NProgress, NTag, NTooltip, NCard, NBreadcrumb, NBreadcrumbItem } from 'naive-ui'
import {
  OpenOutline, ServerOutline, HardwareChipOutline, GlobeOutline, LayersOutline, CubeOutline, ListOutline,
  FlashOutline, ThermometerOutline, TimeOutline, AlertCircleOutline, CheckmarkCircleOutline,
} from '@vicons/ionicons5'
import { api } from '../api'
import { usePoll } from '../poll'
import { t } from '../i18n'
import { ago, bmcURL, gpuShort, healthColor, watts } from '../format'
import HealthBadge from '../components/HealthBadge.vue'
import PowerBadge from '../components/PowerBadge.vue'
import InfoCard from '../components/InfoCard.vue'
import SensorPanel from '../components/SensorPanel.vue'
import EventTable from '../components/EventTable.vue'
import PowerMenu from '../components/PowerMenu.vue'
import ActionTable from '../components/ActionTable.vue'

const props = defineProps<{ name: string }>()
const route = useRoute()
const router = useRouter()
const tab = computed({
  get: () => (route.query.tab as string) || 'overview',
  set: (tab: string) => router.replace({ query: { ...route.query, tab: tab === 'overview' ? undefined : tab } }),
})

const view = usePoll(() => api.get(props.name), 10000)
const live = usePoll(() => api.live(props.name), 10000)
const actions = usePoll(() => api.actions(props.name), 10000)

const v = computed(() => view.data.value)
const s = computed(() => v.value?.status ?? {})
const snap = computed(() => live.data.value)
const errors = computed(() => Object.entries(snap.value?.errors ?? {}))

const resource = computed(() =>
  v.value
    ? JSON.stringify({ apiVersion: 'bmc.kube-bmc.io/v1alpha1', kind: 'BMC', metadata: { name: v.value.name }, spec: v.value.spec, status: v.value.status }, null, 2)
    : '',
)
const selColor = computed(() => {
  const p = s.value.sel?.usedPercent ?? 0
  return p >= 90 ? healthColor.Critical : p >= 70 ? healthColor.Warning : healthColor.OK
})
</script>

<template>
  <main class="page">
    <n-breadcrumb class="crumbs">
      <n-breadcrumb-item><router-link to="/">{{ t('servers') }}</router-link></n-breadcrumb-item>
      <n-breadcrumb-item>{{ name }}</n-breadcrumb-item>
    </n-breadcrumb>

    <n-alert v-if="view.error.value && !v" type="error" :bordered="false">{{ view.error.value }}</n-alert>
    <n-skeleton v-else-if="!v" height="120px" :sharp="false" />

    <template v-else>
      <section class="hero" :style="{ '--accent': healthColor[s.health ?? 'Unknown'] }">
        <div class="hero-main">
          <div class="title-row">
            <h1>{{ v.name }}</h1>
            <HealthBadge :health="s.health" size="medium" />
            <PowerBadge :state="s.powerState" />
            <n-tooltip v-if="v.stale">
              <template #trigger>
                <n-tag size="small" round type="warning" :bordered="false">
                  <template #icon><n-icon :component="TimeOutline" /></template>{{ t('stale') }}
                </n-tag>
              </template>
              {{ t('staleHint') }}
            </n-tooltip>
          </div>
          <div class="subtitle muted">
            {{ s.device?.manufacturer || '—' }} {{ s.device?.product }} · BMC {{ s.controller?.firmwareVersion || '—' }}
            · {{ t('lastSeen') }} {{ ago(v.lastSeen ?? s.lastUpdated) }} {{ t('ago') }}
          </div>
        </div>
        <div class="hero-stats tnum">
          <div><n-icon :component="FlashOutline" /> <b>{{ watts(s.powerWatts) }}</b></div>
          <div><n-icon :component="ThermometerOutline" /> <b>{{ s.inletTemperature != null ? `${s.inletTemperature}°C` : '—' }}</b></div>
        </div>
        <div class="hero-actions">
          <n-button v-if="s.network?.ipAddress" tag="a" :href="bmcURL(s.network.ipAddress)" target="_blank" secondary>
            <template #icon><n-icon :component="OpenOutline" /></template>{{ t('openBMC') }}
          </n-button>
          <PowerMenu :v="v" @done="() => { view.refresh(); actions.refresh() }" />
        </div>
      </section>

      <n-tabs v-model:value="tab" type="line" animated class="tabs">
        <n-tab-pane name="overview" :tab="t('overview')">
          <div class="problems">
            <n-card size="small" :bordered="false" class="pcard">
              <template #header>
                <span class="hdr"><n-icon :component="AlertCircleOutline" />{{ t('problems') }}
                  <span class="muted cnt">{{ s.problems?.length ?? 0 }}</span></span>
              </template>
              <div v-if="!s.problems?.length" class="allgood">
                <n-icon :component="CheckmarkCircleOutline" size="18" /> {{ t('allGood') }}
              </div>
              <ul v-else class="plist">
                <li v-for="(p, i) in s.problems" :key="i" :class="p.severity">
                  <span class="pdot" /><b>{{ p.source }}</b><span>{{ p.message }}</span>
                </li>
              </ul>
              <n-alert v-if="errors.length" type="warning" :bordered="false" :title="t('collectionErrors')" class="cerr">
                <div v-for="[k, e] in errors" :key="k" class="mono">{{ k }}: {{ e }}</div>
              </n-alert>
            </n-card>
          </div>

          <div class="cards">
            <InfoCard :title="t('system')" :icon="ServerOutline" :rows="[
              [t('manufacturer'), s.device?.manufacturer],
              [t('product'), s.device?.product],
              [t('serial'), s.device?.serialNumber, true],
              [t('partNumber'), s.device?.partNumber, true],
              [t('board'), s.device?.boardProduct],
              [t('boardSerial'), s.device?.boardSerial, true],
              [t('chassisType'), s.device?.chassisType],
            ]" />
            <InfoCard :title="t('controller')" :icon="HardwareChipOutline" :rows="[
              [t('firmware'), s.controller?.firmwareVersion, true],
              [t('ipmiVersion'), s.controller?.ipmiVersion],
              [t('manufacturerID'), s.controller?.manufacturerID, true],
              [t('guid'), s.controller?.guid, true],
              ['Agent', v.status.agentVersion, true],
            ]" />
            <InfoCard :title="t('network')" :icon="GlobeOutline" :rows="[
              [t('ipAddress'), s.network?.ipAddress, true],
              [t('macAddress'), s.network?.macAddress, true],
              [t('netmask'), s.network?.netmask, true],
              [t('gateway'), s.network?.gateway, true],
              [t('ipSource'), s.network?.source],
              [t('vlan'), s.network?.vlan],
              [t('channel'), s.network?.channel],
            ]" />
            <InfoCard v-if="v.node" :title="t('kubernetes')" :icon="LayersOutline" :rows="[
              ['Status', `${v.node.ready ? t('nodeReady') : t('notReady')}${v.node.unschedulable ? ` · ${t('cordoned')}` : ''}`],
              [t('roles'), v.node.roles?.join(', ')],
              [t('internalIP'), v.node.internalIP, true],
              [t('gpus'), v.node.gpus ? `${v.node.gpus}× ${gpuShort(v.node.gpuModel) || 'GPU'}` : '—'],
              [`${t('cpu')} / ${t('memory')}`, `${v.node.cpu} / ${v.node.memory}`],
              [t('kubelet'), v.node.kubeletVersion, true],
              [t('os'), v.node.osImage],
              [t('kernel'), v.node.kernelVersion, true],
              [t('agent'), v.agent ? `${v.agent.pod}${v.agent.ready ? '' : ' (not ready)'}` : t('agentDown'), true],
            ]" />
            <InfoCard :title="t('chassis')" :icon="CubeOutline" :rows="[
              [t('restorePolicy'), s.chassis?.powerRestorePolicy],
              [t('lastPowerEvent'), s.chassis?.lastPowerEvent],
              [t('faults'), s.chassis?.faults?.join(', ') || t('none')],
            ]" />
            <InfoCard :title="t('sel')" :icon="ListOutline" :rows="[
              [t('selEntries'), s.sel?.entries ?? 0],
              [t('lastEvent'), s.sel?.lastAddTime, true],
              [t('sensorSummary'), s.sensors ? `${s.sensors.total ?? 0} · ${s.sensors.ok ?? 0} ok · ${s.sensors.warning ?? 0} warn · ${s.sensors.critical ?? 0} crit` : '—'],
            ]">
              <n-progress type="line" :percentage="s.sel?.usedPercent ?? 0" :color="selColor" :height="8" style="margin-top: 14px" />
            </InfoCard>
          </div>
        </n-tab-pane>

        <n-tab-pane name="sensors" :tab="`${t('sensors')}${s.sensors?.total ? ` · ${s.sensors.total}` : ''}`">
          <n-alert v-if="live.error.value && !snap" type="warning" :bordered="false" :title="t('liveUnavailable')">{{ live.error.value }}</n-alert>
          <n-skeleton v-else-if="!snap" text :repeat="8" />
          <SensorPanel v-else :sensors="snap.sensors ?? []" />
        </n-tab-pane>

        <n-tab-pane name="events" :tab="t('events')">
          <n-alert v-if="live.error.value && !snap" type="warning" :bordered="false" :title="t('liveUnavailable')">{{ live.error.value }}</n-alert>
          <n-skeleton v-else-if="!snap" text :repeat="8" />
          <div v-else-if="!snap.events?.length" class="muted noev">{{ t('noEvents') }}</div>
          <EventTable v-else :events="snap.events" />
        </n-tab-pane>

        <n-tab-pane name="actions" :tab="`${t('actionsNav')}${actions.data.value?.length ? ` · ${actions.data.value.length}` : ''}`">
          <n-alert v-if="actions.error.value && !actions.data.value" type="error" :bordered="false">{{ actions.error.value }}</n-alert>
          <div v-else-if="!actions.data.value?.length" class="muted noev">{{ t('noActions') }}</div>
          <ActionTable v-else :actions="actions.data.value" />
        </n-tab-pane>

        <n-tab-pane name="resource" :tab="t('raw')">
          <pre class="raw mono">{{ resource }}</pre>
        </n-tab-pane>
      </n-tabs>
    </template>
  </main>
</template>

<style scoped>
.crumbs { margin-bottom: 14px; }
.crumbs a { text-decoration: none; }
.hero {
  position: relative;
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 18px 32px;
  padding: 20px 24px;
  border-radius: 16px;
  background:
    radial-gradient(120% 140% at 0% 0%, color-mix(in srgb, var(--accent) 12%, transparent), transparent 55%),
    var(--kb-surface);
  border: 1px solid var(--kb-border);
  overflow: hidden;
}
.hero-main { flex: 1 1 380px; min-width: 0; }
.title-row { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
h1 { font-size: 24px; margin: 0; letter-spacing: -0.02em; }
.subtitle { font-size: 13px; margin-top: 6px; }
.hero-stats { display: flex; gap: 24px; font-size: 20px; }
.hero-stats div { display: flex; align-items: center; gap: 8px; }
.hero-stats :deep(.n-icon) { color: var(--kb-muted); font-size: 18px; }
.hero-actions { display: flex; gap: 10px; }
.tabs { margin-top: 18px; }
.problems { margin-bottom: 14px; }
.pcard, .cards :deep(.info) { border: 1px solid var(--kb-border); }
.hdr { display: flex; align-items: center; gap: 8px; font-size: 14px; font-weight: 600; }
.cnt { font-weight: 400; }
.allgood { display: flex; align-items: center; gap: 8px; color: #18a058; font-size: 13.5px; }
.plist { list-style: none; margin: 0; padding: 0; display: grid; gap: 6px; }
.plist li {
  display: flex;
  align-items: baseline;
  gap: 10px;
  font-size: 13.5px;
  padding: 8px 12px;
  border-radius: 8px;
}
.plist li.Critical { background: color-mix(in srgb, #d03050 9%, transparent); }
.plist li.Warning { background: color-mix(in srgb, #f0a020 11%, transparent); }
.pdot { width: 7px; height: 7px; border-radius: 50%; flex-shrink: 0; align-self: center; }
.Critical .pdot { background: #d03050; }
.Warning .pdot { background: #f0a020; }
.cerr { margin-top: 12px; }
.cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(380px, 1fr)); gap: 14px; }
@media (max-width: 640px) { .cards { grid-template-columns: 1fr; } .hero { padding: 16px; } }
.raw {
  margin: 0;
  padding: 16px;
  border-radius: 12px;
  background: var(--kb-surface);
  border: 1px solid var(--kb-border);
  overflow: auto;
  font-size: 12.5px;
  line-height: 1.55;
  max-height: 70vh;
}
.noev { padding: 48px 0; text-align: center; }
</style>
