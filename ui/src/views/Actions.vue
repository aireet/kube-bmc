<script setup lang="ts">
import { NAlert, NEmpty, NSkeleton } from 'naive-ui'
import { api } from '../api'
import { usePoll } from '../poll'
import { t } from '../i18n'
import ActionTable from '../components/ActionTable.vue'

const { data, error, loading } = usePoll(() => api.actions(), 10000)
</script>

<template>
  <main class="page">
    <header class="head">
      <h1>{{ t('actionsTitle') }}</h1>
      <p class="muted">{{ t('actionsHint') }}</p>
    </header>
    <n-alert v-if="error && !data" type="error" :bordered="false">{{ error }}</n-alert>
    <n-skeleton v-else-if="loading" text :repeat="6" />
    <n-empty v-else-if="!data?.length" :description="t('noActions')" style="margin-top: 64px" />
    <ActionTable v-else :actions="data" show-bmc :page-size="25" />
  </main>
</template>

<style scoped>
.head { margin-bottom: 18px; }
h1 { font-size: 22px; margin: 0 0 6px; letter-spacing: -0.02em; }
.head p { margin: 0; font-size: 13.5px; }
</style>
