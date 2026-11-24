<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import InboxMessageText from './InboxMessageText.vue'
import {
  inboxIntegrations,
  type IntegrationCatalogueItem,
} from '@/services/inboxIntegrations'
import { useInboxViewerContext } from '@/composables/useInboxViewerContext'
import type { InboxAction } from '@/services/inbox'

const model = defineModel<InboxAction>({ required: true })
const { t } = useI18n()
const context = useInboxViewerContext()
const items = ref<IntegrationCatalogueItem[]>([]),
  loading = ref(false),
  failed = ref(false)
let generation = 0
const selected = computed(() =>
  items.value.find((item) => item.id === model.value.value),
)
const obsoleteNames = computed(() =>
  selected.value
    ? Object.keys(model.value.integration_inputs ?? {}).filter(
        (name) => !selected.value!.inputs.some((input) => input.name === name),
      )
    : [],
)
async function load() {
  const request = ++generation
  loading.value = true
  failed.value = false
  items.value = []
  try {
    const { data } = await inboxIntegrations.catalogue()
    if (request === generation) items.value = data.data.integrations
  } catch {
    if (request === generation) failed.value = true
  } finally {
    if (request === generation) loading.value = false
  }
}
function changeDefinition() {
  model.value.integration_inputs = {}
}
function inputValue(name: string) {
  const values = model.value.integration_inputs
  return values && Object.prototype.hasOwnProperty.call(values, name)
    ? values[name]!
    : ''
}
function removeMapping(name: string) {
  const values = { ...model.value.integration_inputs }
  delete values[name]
  model.value.integration_inputs = values
}
function setInput(name: string, value: string) {
  const inputs = { ...model.value.integration_inputs }
  if (
    value === '' &&
    !selected.value?.inputs.find((item) => item.name === name)?.required
  )
    delete inputs[name]
  else inputs[name] = value
  model.value.integration_inputs = inputs
}
watch(context, load, { immediate: true, flush: 'sync' })
onUnmounted(() => {
  generation++
})
</script>

<template>
  <div class="grid min-w-0 gap-3">
    <p v-if="loading" role="status" class="inbox-caption">
      {{ t('inboxIntegrations.loadingDefinitions') }}
    </p>
    <p v-if="failed" role="alert" class="inbox-notice">
      {{ t('inboxIntegrations.catalogueError')
      }}<Button type="button" variant="outline" @click="load">{{
        t('common.retry')
      }}</Button>
    </p>
    <label class="inbox-label"
      >{{ t('inboxIntegrations.definition')
      }}<select
        :aria-label="t('inboxIntegrations.definition')"
        v-model="model.value"
        :disabled="loading || failed"
        required
        @change="changeDefinition"
      >
        <option value="">{{ t('inboxIntegrations.chooseDefinition') }}</option>
        <option v-for="item in items" :key="item.id" :value="item.id">
          {{ item.name
          }}{{
            item.enabled ? '' : ' (' + t('inboxIntegrations.disabled') + ')'
          }}
        </option>
        <option
          v-if="model.value && !selected && !loading"
          :value="model.value"
          disabled
        >
          {{ t('inboxIntegrations.definitionUnavailable') }}
        </option>
      </select></label
    >
    <p v-if="!loading && !failed && !items.length" class="inbox-caption">
      {{ t('inboxIntegrations.noDefinitions') }}
    </p>
    <p v-if="selected?.description" class="inbox-caption">
      {{ selected.description }}
    </p>
    <Button
      type="button"
      variant="outline"
      class="justify-self-start"
      :disabled="loading"
      @click="load"
      >{{ t('inboxIntegrations.refreshDefinitions') }}</Button
    >
    <section
      v-if="obsoleteNames.length"
      class="inbox-notice inbox-integration-obsolete gap-2"
      role="status"
    >
      <h3 class="font-medium">{{ t('inboxIntegrations.obsoleteTitle') }}</h3>
      <p class="inbox-caption">{{ t('inboxIntegrations.obsoleteHelp') }}</p>
      <ul class="grid gap-2">
        <li
          v-for="name in obsoleteNames"
          :key="name"
          class="flex flex-wrap items-center gap-2"
        >
          <code class="min-w-0 break-words">{{ name }}</code
          ><Button
            type="button"
            variant="outline"
            @click="removeMapping(name)"
            >{{ t('inboxIntegrations.removeMapping', { name }) }}</Button
          >
        </li>
      </ul>
    </section>
    <p v-if="selected && !selected.enabled" role="status" class="inbox-notice">
      {{ t('inboxIntegrations.definitionDisabled') }}
    </p>
    <p class="inbox-caption">{{ t('inboxIntegrations.mappingHelp') }}</p>
    <div
      v-for="input in selected?.inputs"
      :key="input.name"
      class="grid min-w-0 gap-1"
    >
      <InboxMessageText
        :model-value="inputValue(input.name)"
        @update:model-value="setInput(input.name, $event)"
        :label="
          input.name +
          ' · ' +
          t('inboxFields.kind.' + input.kind) +
          ' · ' +
          t(
            input.required
              ? 'inboxIntegrations.required'
              : 'inboxIntegrations.optional',
          )
        "
        variables
        :required="input.required"
        :rows="2"
      />
      <p class="inbox-caption">
        {{ t('inboxIntegrations.valueHelp.' + input.kind) }}
      </p>
    </div>
  </div>
</template>

<style scoped>
.inbox-integration-obsolete {
  display: grid;
}
</style>
