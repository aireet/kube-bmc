<script setup lang="ts">
import { computed, ref } from 'vue'
import { NButton, NModal, NIcon, NTabs, NTabPane, NTag, useMessage } from 'naive-ui'
import { SparklesOutline, CopyOutline } from '@vicons/ionicons5'
import { config } from '../store'
import { t } from '../i18n'

const message = useMessage()
const open = ref(false)
const endpoint = computed(() => `${location.origin}/mcp`)
const needsToken = computed(() => config.value.auth !== 'none')
const header = computed(() => (needsToken.value ? ' \\\n  --header "Authorization: Bearer $KUBE_BMC_TOKEN"' : ''))

const claude = computed(() => `claude mcp add --transport http kube-bmc ${endpoint.value}${header.value}`)
const json = computed(() =>
  JSON.stringify(
    { mcpServers: { 'kube-bmc': { type: 'http', url: endpoint.value, ...(needsToken.value ? { headers: { Authorization: 'Bearer ${KUBE_BMC_TOKEN}' } } : {}) } } },
    null,
    2,
  ),
)
const token = `kubectl -n kube-bmc-system create serviceaccount mcp-agent
export KUBE_BMC_TOKEN=$(kubectl -n kube-bmc-system create token mcp-agent --duration=24h)`

const tools = [
  ['fleet_summary', 'read'], ['list_servers', 'read'], ['get_server', 'read'], ['get_sensors', 'read'],
  ['get_events', 'read'], ['list_actions', 'read'], ['get_action', 'read'],
  ['locate_server', 'action'], ['power_action', 'destructive'], ['clear_sel', 'destructive'],
] as const

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    message.success(t('copied'))
  } catch {
    message.warning(t('copyFailed'))
  }
}
</script>

<template>
  <n-button quaternary size="small" class="trigger" @click="open = true">
    <template #icon><n-icon :component="SparklesOutline" /></template>{{ t('aiAgents') }}
  </n-button>
  <n-modal v-model:show="open" preset="card" :title="t('aiAgentsTitle')" style="width: min(760px, 94vw)" :bordered="false">
    <p class="lead">{{ t('aiAgentsLead') }}</p>
    <div class="examples">
      <div v-for="q in [t('aiExample1'), t('aiExample2'), t('aiExample3')]" :key="q" class="example">“{{ q }}”</div>
    </div>

    <h4>{{ t('mcpEndpoint') }}</h4>
    <div class="code"><code>{{ endpoint }}</code><n-button text @click="copy(endpoint)"><n-icon :component="CopyOutline" /></n-button></div>

    <n-tabs type="segment" size="small" class="tabs">
      <n-tab-pane name="claude" tab="Claude Code">
        <div class="code"><pre>{{ claude }}</pre><n-button text @click="copy(claude)"><n-icon :component="CopyOutline" /></n-button></div>
      </n-tab-pane>
      <n-tab-pane name="json" tab="mcp.json">
        <div class="code"><pre>{{ json }}</pre><n-button text @click="copy(json)"><n-icon :component="CopyOutline" /></n-button></div>
      </n-tab-pane>
      <n-tab-pane v-if="needsToken" name="token" :tab="t('token')">
        <p class="muted small">{{ t('tokenHint') }}</p>
        <div class="code"><pre>{{ token }}</pre><n-button text @click="copy(token)"><n-icon :component="CopyOutline" /></n-button></div>
      </n-tab-pane>
    </n-tabs>

    <h4>{{ t('mcpTools') }}</h4>
    <div class="tools">
      <n-tag v-for="[name, kind] in tools" :key="name" size="small" round :bordered="false"
        :type="kind === 'destructive' ? 'error' : kind === 'action' ? 'warning' : 'success'">
        <span class="mono">{{ name }}</span>
      </n-tag>
    </div>
    <p class="muted small">{{ t('mcpSafety') }}</p>
  </n-modal>
</template>

<style scoped>
.trigger { font-weight: 600; color: #8a5cf6; }
.lead { margin-top: 0; line-height: 1.6; }
.examples { display: grid; gap: 6px; margin-bottom: 18px; }
.example {
  font-size: 13.5px;
  padding: 8px 12px;
  border-radius: 10px;
  background: color-mix(in srgb, #8a5cf6 8%, transparent);
  border: 1px solid color-mix(in srgb, #8a5cf6 22%, transparent);
}
h4 { margin: 16px 0 8px; font-size: 13.5px; }
.code {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  padding: 10px 12px;
  border-radius: 10px;
  background: color-mix(in srgb, var(--kb-muted) 10%, transparent);
  font-family: var(--kb-mono);
  font-size: 12.5px;
}
.code pre, .code code { margin: 0; flex: 1; white-space: pre-wrap; word-break: break-all; }
.tabs { margin-top: 14px; }
.tools { display: flex; flex-wrap: wrap; gap: 6px; }
.small { font-size: 12.5px; line-height: 1.6; }
</style>
