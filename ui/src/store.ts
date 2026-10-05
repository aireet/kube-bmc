import { ref } from 'vue'
import type { Config, Me } from './types'

export const config = ref<Config>({ version: '', powerActions: false, login: false })
export const me = ref<Me>()
