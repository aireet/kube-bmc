<script setup lang="ts">
import { computed, h, ref } from 'vue'
import { NDataTable, NInput, NIcon, NTag, type DataTableColumns } from 'naive-ui'
import { SearchOutline } from '@vicons/ionicons5'
import type { SELEvent } from '../types'
import { t } from '../i18n'

const props = defineProps<{ events: SELEvent[] }>()
const query = ref('')

const rows = computed(() => {
  const q = query.value.trim().toLowerCase()
  return q ? props.events.filter((e) => `${e.sensor} ${e.event} ${e.detail ?? ''}`.toLowerCase().includes(q)) : props.events
})

const severe = /fail|fault|lost|critical|non-recoverable|uncorrectable|error|trip/i

const columns = computed<DataTableColumns<SELEvent>>(() => [
  { title: '#', key: 'id', width: 70, render: (e) => h('span', { class: 'mono muted' }, e.id) },
  { title: t('time'), key: 'timestamp', width: 210, render: (e) => h('span', { class: 'mono', style: 'white-space:nowrap' }, e.timestamp) },
  {
    title: '', key: 'asserted', width: 96,
    render: (e) => h(NTag, { size: 'small', round: true, bordered: false, type: e.asserted ? (severe.test(e.event) ? 'error' : 'warning') : 'default' },
      () => (e.asserted ? t('asserted') : t('deasserted'))),
  },
  { title: t('sensor'), key: 'sensor', minWidth: 200, ellipsis: { tooltip: true } },
  { title: t('event'), key: 'event', minWidth: 240, ellipsis: { tooltip: true } },
  { title: t('detail'), key: 'detail', minWidth: 240, ellipsis: { tooltip: true }, render: (e) => e.detail || '' },
])
</script>

<template>
  <n-input v-model:value="query" :placeholder="t('filterEvents')" clearable size="small" class="q">
    <template #prefix><n-icon :component="SearchOutline" /></template>
  </n-input>
  <n-data-table :columns="columns" :data="rows" :pagination="{ pageSize: 25 }" size="small" :bordered="false"
    :scroll-x="1000" class="tbl" :row-key="(e: SELEvent) => e.id" />
</template>

<style scoped>
.q { max-width: 280px; margin-bottom: 14px; }
.tbl { background: var(--kb-surface); border: 1px solid var(--kb-border); border-radius: 12px; overflow: hidden; }
</style>
