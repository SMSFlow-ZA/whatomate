<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import { useAuthStore } from '@/stores/auth'
import { useInboxViewerContext } from '@/composables/useInboxViewerContext'
import { inboxIntake, type IntakeMapping } from '@/services/inboxIntake'
import '@/assets/inbox.css'

const props = defineProps<{
  value: string
  stepType: string
  transportBlocked?: boolean
}>()
const emit = defineEmits<{ select: [value: string] }>()
const { t } = useI18n(),
  auth = useAuthStore(),
  viewer = useInboxViewerContext()
const allowed = computed(
  () =>
    ['settings.general', 'flows.chatbot', 'chat', 'contacts'].every(
      (resource) => auth.hasPermission(resource, 'write'),
    ) && auth.hasPermission('contacts', 'read'),
)
const loading = ref(false),
  failed = ref(false),
  incomplete = ref(false),
  mappings = ref<IntakeMapping[]>([])
let generation = 0
const available = computed(() =>
  mappings.value.filter(
    (item) =>
      item.enabled &&
      !item.unavailable_fields?.length &&
      (props.stepType === 'whatsapp_flow' ||
        (item.consent_key !== 'answer' &&
          item.fields.every((field) => field.input === 'answer'))),
  ),
)
async function load() {
  const request = ++generation,
    context = viewer.value
  if (!allowed.value) {
    mappings.value = []
    loading.value = false
    return
  }
  loading.value = true
  failed.value = false
  try {
    const { data } = await inboxIntake.list()
    if (request !== generation || context !== viewer.value) return
    mappings.value = data.data.mappings
    incomplete.value = data.data.has_more
  } catch {
    if (request === generation && context === viewer.value) {
      failed.value = true
      mappings.value = []
    }
  } finally {
    if (request === generation && context === viewer.value)
      loading.value = false
  }
}
watch(() => [viewer.value, allowed.value], load, { immediate: true })
onUnmounted(() => generation++)
</script>
<template>
  <section class="grid gap-2 border-t border-border pt-3">
    <label class="grid gap-1.5 text-sm font-medium"
      >{{ t('inboxIntake.mappings') }}
      <select
        class="inbox-control"
        :value="value"
        :disabled="!allowed || loading || failed || incomplete"
        @change="emit('select', ($event.target as HTMLSelectElement).value)"
      >
        <option value="">{{ t('inboxIntake.noMappingSelected') }}</option>
        <option
          v-if="value && !available.some((item) => item.id === value)"
          :value="value"
        >
          {{ t('inboxIntake.mappingUnavailable') }}
        </option>
        <option
          v-for="item in available"
          :key="item.id"
          :value="item.id"
          :disabled="transportBlocked"
        >
          {{ item.name }}
        </option>
      </select>
    </label>
    <p v-if="transportBlocked" role="alert" class="text-xs text-destructive">
      {{ t('inboxIntake.transportRequired') }}
    </p>
    <p v-if="!allowed" class="text-xs text-muted-foreground">
      {{ t('inboxIntake.mappingAdminRequired') }}
    </p>
    <p v-else-if="loading" role="status" class="text-xs text-muted-foreground">
      {{ t('common.loading') }}
    </p>
    <p
      v-else-if="failed || incomplete"
      role="alert"
      class="text-xs text-destructive"
    >
      {{ t('inboxIntake.mappingLoadFailed')
      }}<Button type="button" size="sm" variant="outline" @click="load">{{
        t('common.retry')
      }}</Button>
    </p>
    <template v-else>
      <p class="text-xs text-muted-foreground">
        {{
          t(
            stepType === 'whatsapp_flow'
              ? 'inboxIntake.formStepHelp'
              : 'inboxIntake.promptStepHelp',
          )
        }}
      </p>
      <RouterLink
        class="text-sm underline underline-offset-4"
        to="/settings/custom-fields"
        >{{ t('inboxIntake.manageMappings') }}</RouterLink
      >
    </template>
  </section>
</template>
