import { ref } from 'vue'
import type { Config, Me } from './types'

export const config = ref<Config>({ version: '', actions: false, auth: 'none' })
export const me = ref<Me>()
