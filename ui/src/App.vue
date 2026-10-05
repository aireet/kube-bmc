<script setup lang="ts">
import { computed, onMounted, ref, watchEffect } from 'vue'
import {
  NConfigProvider, NMessageProvider, NDialogProvider, NButton, NTag, NTooltip, NDropdown, NGlobalStyle,
  darkTheme, useOsTheme, zhCN, dateZhCN, type GlobalThemeOverrides,
} from 'naive-ui'
import { MoonOutline, SunnyOutline, LogoGithub, LanguageOutline } from '@vicons/ionicons5'
import Logo from './components/Logo.vue'
import UserMenu from './components/UserMenu.vue'
import SignedOut from './components/SignedOut.vue'
import { api } from './api'
import { config, me } from './store'
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

const signedOut = new URLSearchParams(location.search).has('signed_out')
const ready = ref(false)

onMounted(async () => {
  try {
    config.value = await api.config()
    if (!signedOut && location.pathname !== '/login') me.value = await api.me()
  } catch {
    /* pages report API errors */
  } finally {
    ready.value = true
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
            <nav class="nav">
              <router-link to="/" class="navlink" exact-active-class="active">{{ t('servers') }}</router-link>
              <router-link to="/actions" class="navlink" active-class="active">{{ t('actionsNav') }}</router-link>
            </nav>
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
            <UserMenu v-if="me && me.method !== 'none'" :me="me" />
          </div>
        </header>
        <SignedOut v-if="signedOut" />
        <router-view v-else-if="ready" />
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
.nav { display: flex; gap: 4px; margin-left: 14px; }
.navlink {
  padding: 6px 12px;
  border-radius: 8px;
  font-size: 14px;
  text-decoration: none;
  color: var(--kb-muted);
  transition: background 0.15s ease, color 0.15s ease;
}
.navlink:hover { color: var(--kb-text); background: color-mix(in srgb, var(--kb-muted) 10%, transparent); }
.navlink.active { color: #18a058; background: color-mix(in srgb, #18a058 10%, transparent); font-weight: 600; }
@media (max-width: 640px) { .nav { margin-left: 4px; } .navlink { padding: 6px 8px; } }
.spacer { flex: 1; }
.version { font-size: 12px; margin-right: 4px; }
</style>
