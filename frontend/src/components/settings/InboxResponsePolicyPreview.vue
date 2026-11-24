<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import { useAuthStore } from '@/stores/auth'
import { useInboxViewerContext } from '@/composables/useInboxViewerContext'
import { inboxService, type QueueCase } from '@/services/inbox'
import {
  responsePolicyService,
  type ResponsePolicyPreview,
} from '@/services/inboxResponsePolicies'
const props = defineProps<{ account: string }>()
const { t } = useI18n(),
  auth = useAuthStore(),
  viewer = useInboxViewerContext()
const context = computed(() => JSON.stringify([viewer.value, props.account]))
const canRead = computed(() =>
  ['settings.general', 'chat', 'contacts'].every((resource) =>
    auth.hasPermission(resource, 'read'),
  ),
)
const preview = ref<ResponsePolicyPreview | null>(null),
  previewBusy = ref(false),
  previewFailed = ref(false)
const caseSearch = ref(''),
  caseId = ref(''),
  cases = ref<QueueCase[]>([]),
  casesBusy = ref(false),
  casesFailed = ref(false),
  casesMore = ref(false),
  caseOffset = ref(0)
let previewGeneration = 0,
  casesGeneration = 0
const sample = computed(() =>
  cases.value.find((row) => row.id === caseId.value),
)
function invalidatePreview() {
  previewGeneration++
  preview.value = null
  previewBusy.value = false
  previewFailed.value = false
}
async function loadCases(offset = 0) {
  if (!canRead.value) return
  const key = context.value,
    search = caseSearch.value,
    generation = ++casesGeneration
  casesBusy.value = true
  casesFailed.value = false
  invalidatePreview()
  try {
    const { data } = await inboxService.queue(
      { account_id: props.account, search },
      offset,
    )
    if (
      key !== context.value ||
      generation !== casesGeneration ||
      search !== caseSearch.value
    )
      return
    cases.value = data.data.cases.filter(
      (row) => row.account_id === props.account,
    )
    caseOffset.value = offset
    casesMore.value = data.data.has_more
    if (!cases.value.some((row) => row.id === caseId.value)) caseId.value = ''
  } catch {
    if (key === context.value && generation === casesGeneration) {
      casesFailed.value = true
      cases.value = []
      caseId.value = ''
    }
  } finally {
    if (key === context.value && generation === casesGeneration)
      casesBusy.value = false
  }
}
async function runPreview() {
  if (!sample.value || !canRead.value) return
  const key = context.value,
    id = caseId.value,
    generation = ++previewGeneration
  previewBusy.value = true
  previewFailed.value = false
  preview.value = null
  try {
    const { data } = await responsePolicyService.preview(props.account, id)
    if (
      key !== context.value ||
      id !== caseId.value ||
      generation !== previewGeneration
    )
      return
    if (
      data.data.account_id !== props.account ||
      data.data.case_id !== id ||
      data.data.prospective_only !== true
    )
      throw new Error('Mismatched policy preview')
    preview.value = data.data
  } catch {
    if (
      key === context.value &&
      id === caseId.value &&
      generation === previewGeneration
    )
      previewFailed.value = true
  } finally {
    if (key === context.value && generation === previewGeneration)
      previewBusy.value = false
  }
}
watch(caseId, invalidatePreview, { flush: 'sync' })
watch(
  caseSearch,
  () => {
    casesGeneration++
    cases.value = []
    caseId.value = ''
    casesMore.value = false
    casesBusy.value = false
    invalidatePreview()
  },
  { flush: 'sync' },
)

watch(
  [context, canRead],
  () => {
    casesGeneration++
    invalidatePreview()
    cases.value = []
    caseId.value = ''
    caseSearch.value = ''
    casesMore.value = false
    casesBusy.value = false
    casesFailed.value = false
    if (canRead.value) void loadCases()
  },
  { immediate: true, flush: 'sync' },
)
onUnmounted(() => {
  casesGeneration++
  previewGeneration++
})
</script>
<template>
  <section class="mt-5 border-t border-border pt-4">
    <h4 class="inbox-section-heading">
      {{ t('inboxPolicy.previewTitle') }}
    </h4>
    <p class="inbox-caption mt-1">{{ t('inboxPolicy.previewHelp') }}</p>
    <form
      class="mt-3 flex flex-wrap items-end gap-2"
      @submit.prevent="loadCases()"
    >
      <label class="inbox-label min-w-0 flex-1"
        >{{ t('inboxPolicy.findCase')
        }}<input v-model="caseSearch" type="search" /></label
      ><Button type="submit" variant="outline" :disabled="casesBusy">{{
        t('common.search')
      }}</Button>
    </form>
    <p v-if="casesBusy" class="mt-3" role="status">
      {{ t('common.loading') }}
    </p>
    <p v-else-if="casesFailed" class="inbox-notice mt-3" role="alert">
      {{ t('inboxPolicy.caseError')
      }}<Button type="button" variant="outline" @click="loadCases()">{{
        t('common.retry')
      }}</Button>
    </p>
    <template v-else
      ><label class="inbox-label mt-3"
        >{{ t('inboxPolicy.sample')
        }}<select v-model="caseId">
          <option value="">{{ t('inboxPolicy.chooseCase') }}</option>
          <option v-for="row in cases" :key="row.id" :value="row.id">
            {{ row.contact_name }} · {{ row.id.slice(0, 8) }} ·
            {{ t('inboxWorkflow.status.' + row.status) }}
          </option>
        </select></label
      >
      <p v-if="!cases.length" class="inbox-caption mt-2">
        {{ t('inboxPolicy.noCases') }}
      </p>
      <div class="mt-2 flex gap-2">
        <Button
          v-if="caseOffset > 0"
          type="button"
          variant="outline"
          @click="loadCases(Math.max(0, caseOffset - 50))"
          >{{ t('common.previous') }}</Button
        ><Button
          v-if="casesMore"
          type="button"
          variant="outline"
          @click="loadCases(caseOffset + 50)"
          >{{ t('common.next') }}</Button
        >
      </div></template
    >
    <Button
      class="mt-3"
      type="button"
      :disabled="!sample || casesBusy || previewBusy"
      @click="runPreview"
      >{{
        previewBusy ? t('common.loading') : t('inboxPolicy.runPreview')
      }}</Button
    >
    <p v-if="previewFailed" class="inbox-notice mt-3" role="alert">
      {{ t('inboxPolicy.previewError') }}
    </p>
    <div v-if="preview" class="mt-4" aria-live="polite">
      <p class="font-medium">
        {{ t('inboxPolicy.prospective') }}:
        {{
          preview.evaluations.find((item) => item.selected)?.name ||
          (preview.default_policy
            ? t('inboxPolicy.default')
            : t('inboxPolicy.noTargets'))
        }}
      </p>
      <p class="inbox-caption mt-2">
        {{
          t('inboxPolicy.retainedVersion', {
            version:
              preview.existing_policy_version || t('inboxPolicy.noTargets'),
          })
        }}
      </p>
      <RouterLink
        v-if="sample"
        class="mt-2 inline-block min-h-11 py-2 underline underline-offset-4"
        :to="{
          name: 'chat-conversation',
          params: { contactId: sample.contact_id },
          query: {
            inbox_account: sample.account_name,
            inbox_account_id: account,
            case_id: sample.id,
          },
        }"
        >{{ t('inboxPolicy.openSample') }}</RouterLink
      >
      <ol class="divide-y divide-border">
        <li
          v-for="item in preview.evaluations"
          :key="item.policy_id"
          class="py-3"
        >
          <p class="font-medium">{{ item.position + 1 }}. {{ item.name }}</p>
          <p class="inbox-caption mt-1">
            {{ t('inboxPolicy.reason.' + item.reason) }}
          </p>
          <ul class="mt-2 grid gap-1 text-sm">
            <li v-for="(condition, index) in item.conditions" :key="index">
              {{
                condition.label ||
                t(
                  condition.field === 'team'
                    ? 'inboxPolicy.team'
                    : 'inboxWorkflow.casePriority',
                )
              }}:
              {{
                condition.matched
                  ? t('inboxPolicy.matched')
                  : t('inboxPolicy.notMatched')
              }}
            </li>
          </ul>
        </li>
      </ol>
    </div>
  </section>
</template>
