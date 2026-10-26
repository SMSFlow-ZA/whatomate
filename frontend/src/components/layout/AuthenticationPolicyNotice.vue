<script setup lang="ts">
import { extensionSession } from '@/extensions/session'
</script>

<template>
  <p v-if="extensionSession.ready && extensionSession.authenticationPolicy === 'primary-session-demo'" role="status" class="border-b border-amber-500/40 bg-amber-500/10 px-4 py-3 text-sm text-amber-200 light:text-amber-900">
    Demo access: password sign-in; MFA temporarily not required.
  </p>
</template>
