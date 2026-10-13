<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { api } from '@/services/api'

type PlanStatus = {
  enabled: boolean
  enforced: boolean
  limits: { max_organizations: number; max_users: number; max_numbers: number; max_storage_bytes: number }
  usage: { organizations: number; users: number; numbers: number; storage_bytes: number }
  over_limit: { users: boolean; numbers: boolean; storage: boolean }
}
const route = useRoute()
const status = ref<PlanStatus | null>(null)
const refreshing = ref(false)
const unavailable = ref(false)
const aboveLimit = computed(() => status.value && Object.values(status.value.over_limit).some(Boolean))
const gib = (value: number) => (value / 1073741824).toLocaleString(undefined, { maximumFractionDigits: 2 })

async function refresh() {
  if (refreshing.value) return
  refreshing.value = true
  try {
    const response = await api.get('/plan')
    status.value = response.data.data.enabled ? response.data.data : null
    unavailable.value = false
  } catch {
    unavailable.value = true
  } finally {
    refreshing.value = false
  }
}
onMounted(refresh)
watch(() => route.fullPath, refresh)
</script>

<template>
  <section v-if="status?.enforced" aria-label="Plan usage" class="shrink-0 border-b border-white/10 light:border-gray-200 bg-emerald-950/30 light:bg-emerald-50 px-4 py-2 text-xs text-white/80 light:text-gray-700">
    <div class="flex flex-wrap items-center gap-x-5 gap-y-1">
      <strong>Plan usage</strong>
      <span>{{ status.usage.organizations }} / {{ status.limits.max_organizations }} organization</span>
      <span>{{ status.usage.users }} / {{ status.limits.max_users }} customer users</span>
      <span>{{ status.usage.numbers }} / {{ status.limits.max_numbers }} WhatsApp numbers</span>
      <span>{{ gib(status.usage.storage_bytes) }} / {{ gib(status.limits.max_storage_bytes) }} GiB media</span>
      <button type="button" class="underline underline-offset-2 disabled:opacity-50" :disabled="refreshing" @click="refresh">Refresh usage</button>
    </div>
    <p v-if="aboveLimit" role="alert" class="mt-1">New additions are blocked while usage exceeds the current plan. Existing data remains available.</p>
    <p v-if="unavailable" role="status" class="mt-1">Usage could not be refreshed. Plan limits still apply.</p>
  </section>
</template>
