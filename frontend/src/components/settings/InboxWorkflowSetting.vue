<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from '@/lib/toast'
import { useAuthStore } from '@/stores/auth'
import { useOrganizationsStore } from '@/stores/organizations'
import { inboxService } from '@/services/inbox'

const auth = useAuthStore()
const organizations = useOrganizationsStore()
const organizationId = computed(() => organizations.selectedOrganization?.id || auth.organizationId)
const { t } = useI18n()
const enabled = ref(false)
const ready = ref(false)
const busy = ref(false)

watch(organizationId, async (org) => {
  ready.value = false
  if (!org) return
  try {
    const { data } = await inboxService.settings()
    if (org !== organizationId.value) return
    enabled.value = data.data.enabled
    ready.value = true
  } catch {
    if (org === organizationId.value) toast.error(t('inboxWorkflow.settingsError'))
  }
}, { immediate: true })

async function change(event: Event) {
  const control = event.target as HTMLInputElement
  const org = organizationId.value
  const next = control.checked
  control.checked = enabled.value
  busy.value = true
  try {
    const { data } = await inboxService.setEnabled(next)
    if (org === organizationId.value) enabled.value = data.data.enabled
  } catch {
    if (org === organizationId.value) toast.error(t('inboxWorkflow.settingsError'))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <label v-if="ready" class="flex flex-wrap items-center gap-2 mt-3">
    <input type="checkbox" role="switch" :checked="enabled" :disabled="busy" @change="change" />
    <span>{{ t('inboxWorkflow.enable') }}</span>
    <small class="text-light">{{ t('inboxWorkflow.description') }}</small>
  </label>
</template>
