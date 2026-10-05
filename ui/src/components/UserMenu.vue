<script setup lang="ts">
import { computed, h } from 'vue'
import { NDropdown, NAvatar, NTag } from 'naive-ui'
import type { Me } from '../types'
import { t } from '../i18n'

const props = defineProps<{ me: Me }>()

const display = computed(() => props.me.name || props.me.email || props.me.username)
const initials = computed(() =>
  display.value
    .replace(/^[^:]*:/, '')
    .split(/[\s@._-]+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((p) => p[0]!.toUpperCase())
    .join(''),
)

const options = computed(() => [
  {
    key: 'header',
    type: 'render',
    render: () =>
      h('div', { class: 'who' }, [
        h('div', { class: 'who-name' }, display.value),
        h('div', { class: 'who-user mono' }, props.me.username),
        h('div', { class: 'who-tags' },
          (props.me.groups ?? []).slice(0, 6).map((g) => h(NTag, { size: 'small', round: true, bordered: false }, () => g))),
      ]),
  },
  { key: 'divider', type: 'divider' },
  { key: 'logout', label: t('signOut') },
])

function select(key: string) {
  if (key === 'logout') location.assign('/auth/logout')
}
</script>

<template>
  <n-dropdown :options="options" trigger="click" placement="bottom-end" @select="select">
    <n-avatar round size="small" class="avatar">{{ initials || '?' }}</n-avatar>
  </n-dropdown>
</template>

<style scoped>
.avatar { cursor: pointer; background: #18a058; color: #fff; font-weight: 600; margin-left: 4px; }
:global(.who) { padding: 10px 14px 8px; max-width: 300px; }
:global(.who-name) { font-weight: 600; }
:global(.who-user) { color: var(--kb-muted); font-size: 12px; margin: 2px 0 8px; word-break: break-all; }
:global(.who-tags) { display: flex; flex-wrap: wrap; gap: 4px; }
</style>
