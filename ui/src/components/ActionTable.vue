<script setup lang="ts">
import { computed, h } from 'vue'
import { NDataTable, NTag, NTooltip, type DataTableColumns } from 'naive-ui'
import type { ActionPhase, BMCAction } from '../types'
import { ago } from '../format'
import { t } from '../i18n'

const props = defineProps<{ actions: BMCAction[]; showBmc?: boolean; pageSize?: number }>()

const phaseType: Record<ActionPhase, 'default' | 'info' | 'success' | 'error' | 'warning'> = {
  Pending: 'default', Running: 'info', Succeeded: 'success', Failed: 'error', Rejected: 'warning',
}

const columns = computed<DataTableColumns<BMCAction>>(() => [
  ...(props.showBmc
    ? [{ title: t('server'), key: 'bmc', width: 150, render: (a: BMCAction) => h('b', a.spec.bmcName) }]
    : []),
  { title: t('actionCol'), key: 'action', width: 150, render: (a) => t(`action.${a.spec.action}`) },
  {
    title: t('phase'), key: 'phase', width: 110,
    render: (a) => {
      const p = a.status?.phase ?? 'Pending'
      return h(NTag, { size: 'small', round: true, bordered: false, type: phaseType[p] }, () => p)
    },
  },
  { title: t('requestedBy'), key: 'requestedBy', minWidth: 200, ellipsis: { tooltip: true }, render: (a) => h('span', { class: 'mono' }, a.spec.requestedBy) },
  { title: t('reason'), key: 'reason', minWidth: 200, ellipsis: { tooltip: true }, render: (a) => a.spec.reason ?? '' },
  { title: t('message'), key: 'message', minWidth: 240, ellipsis: { tooltip: true }, render: (a) => a.status?.message ?? '' },
  {
    title: t('created'), key: 'created', width: 110,
    render: (a) => h(NTooltip, null, {
      trigger: () => h('span', `${ago(a.metadata.creationTimestamp)} ${t('ago')}`),
      default: () => new Date(a.metadata.creationTimestamp).toLocaleString(),
    }),
  },
])
</script>

<template>
  <n-data-table :columns="columns" :data="actions" size="small" :bordered="false" :scroll-x="1100"
    :pagination="actions.length > (pageSize ?? 10) ? { pageSize: pageSize ?? 10 } : false"
    :row-key="(a: BMCAction) => a.metadata.name" class="tbl" />
</template>

<style scoped>
.tbl { background: var(--kb-surface); border: 1px solid var(--kb-border); border-radius: 12px; overflow: hidden; }
</style>
