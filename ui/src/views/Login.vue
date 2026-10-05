<script setup lang="ts">
import { ref } from 'vue'
import { useRoute } from 'vue-router'
import { NCard, NForm, NFormItem, NInput, NButton, NAlert } from 'naive-ui'
import Logo from '../components/Logo.vue'
import { api } from '../api'
import { config } from '../store'
import { t } from '../i18n'

const route = useRoute()
const username = ref('')
const password = ref('')
const busy = ref(false)
const error = ref('')

function target(): string {
  const rd = String(route.query.rd ?? '/')
  return rd.startsWith('/') && !rd.startsWith('//') ? rd : '/'
}

async function submit() {
  if (!username.value || !password.value) return
  busy.value = true
  error.value = ''
  try {
    await api.login(username.value, password.value)
    location.assign(target())
  } catch (e) {
    const msg = (e as Error).message
    error.value = msg === 'invalid username or password' ? t('invalidCredentials') : msg
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <main class="wrap">
    <n-card class="card" :bordered="false">
      <div class="head">
        <Logo :size="40" />
        <h1>{{ t('signInTitle') }}</h1>
        <p v-if="config.clusterName" class="muted">{{ config.clusterName }}</p>
      </div>
      <n-alert v-if="config.auth === 'oidc'" type="info" :bordered="false">
        <a href="/auth/login?rd=/">{{ t('signIn') }} →</a>
      </n-alert>
      <n-form v-else @submit.prevent="submit">
        <n-form-item :label="t('username')" :show-feedback="false" class="field">
          <n-input v-model:value="username" autofocus :input-props="{ autocomplete: 'username', name: 'username' }" />
        </n-form-item>
        <n-form-item :label="t('password')" :show-feedback="false" class="field">
          <n-input v-model:value="password" type="password" show-password-on="click"
            :input-props="{ autocomplete: 'current-password', name: 'password' }" />
        </n-form-item>
        <n-alert v-if="error" type="error" :bordered="false" class="field">{{ error }}</n-alert>
        <n-button type="primary" block :loading="busy" :disabled="!username || !password" attr-type="submit">
          {{ t('signIn') }}
        </n-button>
      </n-form>
    </n-card>
  </main>
</template>

<style scoped>
.wrap { min-height: calc(100vh - 56px); display: grid; place-items: center; padding: 24px 16px; }
.card { width: 100%; max-width: 380px; border: 1px solid var(--kb-border); background: var(--kb-surface); }
.head { text-align: center; margin-bottom: 22px; }
h1 { font-size: 20px; margin: 12px 0 4px; letter-spacing: -0.01em; }
.head p { margin: 0; font-size: 13px; }
.field { margin-bottom: 14px; }
</style>
