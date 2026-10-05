<script setup lang="ts">
import { NButton, NResult } from 'naive-ui'
import { t } from '../i18n'
</script>

<template>
  <main class="page">
    <n-result status="info" :title="t('signedOut')" :description="t('signedOutHint')" style="margin-top: 12vh">
      <template #footer>
        <n-button type="primary" tag="a" href="/auth/login?rd=/">{{ t('signIn') }}</n-button>
      </template>
    </n-result>
  </main>
</template>
