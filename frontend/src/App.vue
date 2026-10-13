<script setup lang="ts">
import { RouterView } from 'vue-router'
import { Toaster } from 'vue-sonner'
import { TooltipProvider } from '@/components/ui/tooltip'
import { useColorMode } from '@/composables/useColorMode'

// Initialize color mode early
const { colorMode } = useColorMode()
const sourcePath = `${((window as any).__BASE_PATH__ ?? '').replace(/\/$/, '')}/source`
</script>

<template>
  <TooltipProvider>
    <div class="min-h-screen bg-background font-sans antialiased">
      <RouterView />
      <a :href="sourcePath" target="_blank" rel="noopener noreferrer" class="fixed bottom-1 right-3 z-10 rounded bg-background/95 px-2 py-1 text-xs text-emerald-500 light:text-emerald-800 underline underline-offset-2">Source code (AGPL)</a>
      <Toaster position="top-right" richColors :theme="colorMode" offset="72px" />
    </div>
  </TooltipProvider>
</template>
