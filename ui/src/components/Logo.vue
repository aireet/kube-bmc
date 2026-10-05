<script setup lang="ts">
defineProps<{ size?: number }>()
</script>

<template>
  <svg :width="size ?? 28" :height="size ?? 28" viewBox="0 0 64 64" aria-hidden="true">
    <defs>
      <linearGradient id="kb-logo" x1="0" y1="0" x2="1" y2="1">
        <stop offset="0" stop-color="#36ad6a" />
        <stop offset="1" stop-color="#0c7a43" />
      </linearGradient>
    </defs>
    <rect x="4" y="4" width="56" height="56" rx="14" fill="url(#kb-logo)" />
    <rect x="15" y="17" width="34" height="9" rx="2.5" fill="#fff" />
    <rect x="15" y="29" width="34" height="9" rx="2.5" fill="#fff" opacity=".85" />
    <rect x="15" y="41" width="34" height="9" rx="2.5" fill="#fff" opacity=".7" />
    <circle cx="20" cy="21.5" r="1.8" fill="#18a058" />
    <circle cx="20" cy="33.5" r="1.8" fill="#18a058" />
    <circle cx="20" cy="45.5" r="1.8" fill="#f0a020" />
    <path d="M33 21.5h11M33 33.5h11M33 45.5h11" stroke="#18a058" stroke-width="2" stroke-linecap="round" opacity=".5" />
  </svg>
</template>
