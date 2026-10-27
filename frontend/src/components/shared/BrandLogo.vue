<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { defaultBranding, useBrandingStore } from '@/stores/branding'
const props = defineProps<{ compact?: boolean }>()
const branding = useBrandingStore(), failed = ref(false)
// A host-provided workspace name is a text identity, not an uploaded logo.
// Avoid squeezing it beside an unrelated stock wordmark in the narrow header.
const textIdentity = computed(() => !branding.current.custom && !['Whatomate', 'SMSFlow'].includes(branding.current.display_name))
const url = computed(() => props.compact ? branding.current.favicon_url.includes('/api/') ? branding.current.favicon_url : branding.current.logo_url.includes('/api/') ? branding.current.logo_url : branding.current.favicon_url : branding.current.logo_url)
watch(url, () => { failed.value = false })
</script>
<template>
  <span class="inline-flex min-w-0 max-w-full items-center gap-2" :title="branding.current.display_name">
    <span v-if="compact || !textIdentity" class="inline-flex items-center justify-center rounded-md bg-white/95 p-1">
      <img :src="failed ? (compact ? (branding.current.default_favicon_url || defaultBranding.favicon_url) : (branding.current.default_logo_url || defaultBranding.logo_url)) : url"
        :alt="branding.current.display_name" :class="compact ? 'h-7 w-7 object-contain' : 'h-7 max-w-[112px] object-contain'" @error="failed = true" />
    </span>
    <span v-if="!compact && (textIdentity || branding.current.custom)" class="min-w-0 text-sm font-semibold text-foreground" :class="textIdentity ? 'whitespace-normal break-words leading-tight' : 'max-w-[140px] truncate'">{{ branding.current.display_name }}</span>
  </span>
</template>
