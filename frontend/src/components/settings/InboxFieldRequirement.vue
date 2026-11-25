<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import InboxConditionBuilder from '@/components/chat/InboxConditionBuilder.vue'
import { accountsService } from '@/services/api'
import { useTeamsStore } from '@/stores/teams'
import type { FieldRequirement, InboxFilters } from '@/services/inbox'

const model = defineModel<FieldRequirement>({ required: true })
const emit = defineEmits<{ validity: [value: boolean] }>()
const { t } = useI18n(),
  teamsStore = useTeamsStore()
const accounts = ref<{ id: string; name: string }[]>([])
const teams = ref<{ id: string; name: string }[]>([])
const loading = ref(false),
  failed = ref(false),
  conditionsValid = ref(true)
let generation = 0
const predicates = computed<InboxFilters>({
  get: () => model.value,
  set: (value) => {
    model.value = {
      ...model.value,
      match: value.match,
      predicates: value.predicates ?? [],
    }
  },
})
const valid = computed(
  () =>
    !loading.value &&
    !failed.value &&
    conditionsValid.value &&
    (!model.value.account_id ||
      accounts.value.some((a) => a.id === model.value.account_id)) &&
    (!model.value.team_id ||
      teams.value.some((a) => a.id === model.value.team_id)) &&
    !!(
      model.value.account_id ||
      model.value.team_id ||
      model.value.predicates.length
    ),
)
// The child condition builder can mutate the shared array; deep validity also
// tracks its selected schema without discarding an administrator's draft.
watch(valid, (value) => emit('validity', value), {
  immediate: true,
  flush: 'sync',
})
async function load() {
  const request = ++generation
  loading.value = true
  failed.value = false
  try {
    const [wa, group] = await Promise.all([
      accountsService.list(),
      teamsStore.fetchTeams({ limit: 100 }),
    ])
    if (request !== generation) return
    accounts.value = wa.data.data?.accounts ?? []
    // Do not label a truncated team list as a complete set of choices.
    if (group.total > group.teams.length)
      throw new Error('incomplete_team_choices')
    teams.value = group.teams.filter((team) => team.is_active !== false)
  } catch {
    if (request === generation) failed.value = true
  } finally {
    if (request === generation) loading.value = false
  }
}
onMounted(load)
onUnmounted(() => generation++)
</script>
<template>
  <div class="grid min-w-0 gap-4">
    <p class="inbox-caption">{{ t('inboxIntake.scopeHelp') }}</p>
    <p v-if="loading" role="status" class="inbox-caption">
      {{ t('common.loading') }}
    </p>
    <p v-else-if="failed" role="alert" class="inbox-notice">
      {{ t('inboxIntake.choicesFailed') }}
      <Button type="button" variant="outline" size="sm" @click="load">{{
        t('common.retry')
      }}</Button>
    </p>
    <div class="inbox-grid">
      <label class="inbox-label"
        >{{ t('inboxQueue.account') }}
        <select v-model="model.account_id" :disabled="loading || failed">
          <option :value="undefined">{{ t('inboxSla.any') }}</option>
          <option
            v-if="
              model.account_id &&
              !accounts.some((a) => a.id === model.account_id)
            "
            :value="model.account_id"
          >
            {{ t('inboxIntake.unavailableAccount') }}
          </option>
          <option
            v-for="account in accounts"
            :key="account.id"
            :value="account.id"
          >
            {{ account.name }}
          </option>
        </select>
      </label>
      <label class="inbox-label"
        >{{ t('inboxQueue.team') }}
        <select v-model="model.team_id" :disabled="loading || failed">
          <option :value="undefined">{{ t('inboxSla.any') }}</option>
          <option
            v-if="model.team_id && !teams.some((a) => a.id === model.team_id)"
            :value="model.team_id"
          >
            {{ t('inboxIntake.unavailableTeam') }}
          </option>
          <option v-for="team in teams" :key="team.id" :value="team.id">
            {{ team.name }}
          </option>
        </select>
      </label>
    </div>
    <InboxConditionBuilder
      v-model="predicates"
      :allowed-fields="['priority', 'custom_field']"
      active-definitions-only
      :help="t('inboxIntake.conditionHelp')"
      @validity="conditionsValid = $event"
    />
    <p v-if="!loading && !failed && !valid" role="status" class="inbox-caption">
      {{ t('inboxIntake.conditionRequired') }}
    </p>
  </div>
</template>
