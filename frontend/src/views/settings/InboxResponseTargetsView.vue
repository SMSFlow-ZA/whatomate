<script setup lang="ts">
import { PageHeader } from '@/components/shared'
import InboxSLASettings from '@/components/settings/InboxSLASettings.vue'
</script>
<template>
  <main class="inbox-ui inbox-page">
    <PageHeader :title="$t('inboxUX.responseTargets')" back-link="/settings" />
    <div class="inbox-page-scroll">
      <div class="inbox-container"><InboxSLASettings /></div>
    </div>
  </main>
</template>
