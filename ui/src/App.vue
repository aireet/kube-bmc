<script setup lang="ts">
import { computed, onMounted, ref, watchEffect } from 'vue'
import {
  NConfigProvider, NMessageProvider, NDialogProvider, NButton, NTag, NTooltip, NDropdown, NGlobalStyle,
  darkTheme, useOsTheme, zhCN, dateZhCN, type GlobalThemeOverrides,
} from 'naive-ui'
import { MoonOutline, SunnyOutline, LogoGithub, LanguageOutline } from '@vicons/ionicons5'
import Logo from './components/Logo.vue'
import { api } from './api'
import { config } from './store'
import { locale, setLocale, t } from './i18n'

type Mode = 'auto' | 'light' | 'dark'
const os = useOsTheme()
const mode = ref<Mode>(read('kube-bmc.theme', 'auto') as Mode)
const dark = computed(() => (mode.value === 'auto' ? os.value === 'dark' : mode.value === 'dark'))
watchEffect(() => document.documentElement.setAttribute('data-theme', dark.value ? 'dark' : 'light'))

function read(key: string, fallback: string) {
  try {
    return localStorage.getItem(key) ?? fallback
  } catch {
    return fallback
  }
}
function toggleTheme() {
  mode.value = dark.value ? 'light' : 'dark'
  try {
    localStorage.setItem('kube-bmc.theme', mode.value)
  } catch {
    /* storage unavailable */
  }
}

const overrides: GlobalThemeOverrides = {
  common: {
    primaryColor: '#18a058',
    primaryColorHover: '#36ad6a',
    primaryColorPressed: '#0c7a43',
    borderRadius: '8px',
    fontFamily: "-apple-system, BlinkMacSystemFont, 'Segoe UI', 'Inter', 'PingFang SC', 'Microsoft YaHei', sans-serif",
  },
  Card: { borderRadius: '12px' },
}
const darkOverrides: GlobalThemeOverrides = {
  ...overrides,
  common: { ...overrides.common, bodyColor: '#0f1115', cardColor: '#17191f', modalColor: '#1c1f26', popoverColor: '#1c1f26' },
}

const langOptions = [
  { label: 'English', key: 'en' },
  { label: '简体中文', key: 'zh' },
]

onMounted(async () => {
  try {
    config.value = await api.config()
  } catch {
    /* the pages surface API errors themselves */
  }
})
</script>

<template>
  <n-config-provider
    :theme="dark ? darkTheme : null"
    :theme-overrides="dark ? darkOverrides : overrides"
    :locale="locale === 'zh' ? zhCN : null"
    :date-locale="locale === 'zh' ? dateZhCN : null"
  >
    <n-global-style />
    <n-message-provider>
      <n-dialog-provider>
        <header class="topbar">
          <div class="topbar-inner">
            <router-link to="/" class="brand">
              <Logo :size="28" />
              <span class="brand-name">kube-bmc</span>
            </router-link>
            <n-tag v-if="config.clusterName" size="small" round :bordered="false">{{ config.clusterName }}</n-tag>
            <n-tag v-if="config.demo" size="small" round type="warning" :bordered="false">{{ t('demo') }}</n-tag>
            <div class="spacer" />
            <span v-if="config.version" class="version mono muted">{{ config.version }}</span>
            <n-dropdown :options="langOptions" :value="locale" @select="setLocale">
              <n-button quaternary circle :aria-label="'Language'">
                <template #icon><LanguageOutline /></template>
              </n-button>
            </n-dropdown>
            <n-tooltip>
              <template #trigger>
                <n-button quaternary circle aria-label="Toggle theme" @click="toggleTheme">
                  <template #icon><SunnyOutline v-if="dark" /><MoonOutline v-else /></template>
                </n-button>
              </template>
              {{ dark ? 'Light' : 'Dark' }}
            </n-tooltip>
            <n-button quaternary circle tag="a" href="https://github.com/aireet/kube-bmc" target="_blank" aria-label="GitHub">
              <template #icon><LogoGithub /></template>
            </n-button>
          </div>
        </header>
        <router-view />
      </n-dialog-provider>
    </n-message-provider>
  </n-config-provider>
</template>

<style scoped>
.topbar {
  position: sticky;
  top: 0;
  z-index: 10;
  backdrop-filter: saturate(180%) blur(12px);
  background: color-mix(in srgb, var(--kb-bg) 78%, transparent);
  border-bottom: 1px solid var(--kb-border);
}
.topbar-inner {
  max-width: 1480px;
  margin: 0 auto;
  height: 56px;
  padding: 0 24px;
  display: flex;
  align-items: center;
  gap: 10px;
}
@media (max-width: 640px) { .topbar-inner { padding: 0 12px; } .version { display: none; } }
.brand { display: flex; align-items: center; gap: 10px; text-decoration: none; }
.brand-name { font-weight: 650; font-size: 17px; letter-spacing: -0.01em; }
.spacer { flex: 1; }
.version { font-size: 12px; margin-right: 4px; }
</style>
