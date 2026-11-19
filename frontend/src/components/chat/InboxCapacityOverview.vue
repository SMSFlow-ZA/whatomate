<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { useTeamsStore } from '@/stores/teams'
import InboxTeamCapacity from '@/components/settings/InboxTeamCapacity.vue'

const { t } = useI18n()
const auth = useAuthStore()
const teamsStore = useTeamsStore()
const teams = ref<{ id: string; name: string }[]>([])
const selected = ref('')
const failed = ref(false)
const loading = ref(true)
async function load() {
  loading.value = true
  failed.value = false
  try {
    teams.value = (await teamsStore.fetchTeams()).teams
  } catch {
    failed.value = true
  } finally {
    loading.value = false
  }
}
onMounted(load)
</script>

<template>
  <section class="inbox-ui" :aria-label="t('inboxCapacity.title')">
    <div class="inbox-toolbar">
      <div>
        <h2 class="inbox-section-heading">{{ t('inboxCapacity.title') }}</h2>
        <p class="inbox-caption mt-1">{{ t('inboxUX.capacityDescription') }}</p>
      </div>
      <label v-if="teams.length" class="inbox-label"
        >{{ t('inboxQueue.team')
        }}<select v-model="selected">
          <option value="">{{ t('inboxUX.chooseTeam') }}</option>
          <option v-for="team in teams" :key="team.id" :value="team.id">
            {{ team.name }}
          </option>
        </select></label
      >
    </div>
    <p v-if="failed" role="alert">
      {{ t('inboxQueue.loadFiltersError') }}
      <button type="button" class="outline small" @click="load">
        {{ t('common.retry') }}
      </button>
    </p>
    <p v-else-if="loading" role="status">{{ t('common.loading') }}</p>
    <InboxTeamCapacity
      v-else-if="selected"
      :key="selected"
      :team="selected"
      embedded
      :can-write="auth.hasPermission('teams', 'write')"
    />
    <p v-else class="inbox-caption">{{ t('inboxUX.chooseTeam') }}</p>
  </section>
</template>
