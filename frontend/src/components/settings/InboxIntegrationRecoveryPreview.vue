<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { FieldValue } from '@/services/inbox'
import type { IntegrationRecoveryPreview } from '@/services/inboxIntegrations'
defineProps<{ preview: IntegrationRecoveryPreview }>()
const { t } = useI18n()
function value(input: FieldValue | undefined) {
  if (input === null || input === undefined)
    return t('inboxInvestigation.empty')
  if (typeof input === 'boolean') return t(input ? 'common.yes' : 'common.no')
  return typeof input === 'string' || typeof input === 'number'
    ? String(input)
    : t('inboxRecovery.unavailable')
}
</script>
<template>
  <div class="grid min-w-0 gap-3 text-sm">
    <p class="inbox-notice">
      {{
        t(
          preview.manual_attestation
            ? 'inboxInvestigation.manualPreviewHelp'
            : preview.action === 'discard_outputs'
              ? 'inboxInvestigation.discardHelp'
              : 'inboxInvestigation.applyHelp',
        )
      }}
    </p>
    <p
      v-if="preview.manual_attestation && preview.fact_only"
      class="inbox-caption"
    >
      {{ t('inboxInvestigation.factOnly') }}
    </p>
    <dl v-if="preview.evidence_reference" class="grid gap-1">
      <dt class="inbox-caption">{{ t('inboxInvestigation.reference') }}</dt>
      <dd class="whitespace-pre-wrap break-words">
        {{ preview.evidence_reference }}
      </dd>
    </dl>
    <dl
      v-if="
        preview.result_reference !== null &&
        preview.result_reference !== undefined
      "
      class="grid gap-1"
    >
      <dt class="inbox-caption">
        {{ t('inboxInvestigation.resultReference') }}
      </dt>
      <dd class="whitespace-pre-wrap break-words">
        {{ value(preview.result_reference) }}
      </dd>
    </dl>
    <dl
      v-for="field in preview.field_changes"
      :key="field.id"
      class="grid min-w-0 gap-1 border-t border-border pt-3"
    >
      <dt class="font-medium break-words">
        {{ field.label }} · {{ t('inboxFields.scope.' + field.scope) }}
      </dt>
      <template v-if="!preview.manual_attestation"
        ><dt class="inbox-caption">
          {{ t('inboxInvestigation.currentValue') }}
        </dt>
        <dd class="max-h-40 overflow-auto whitespace-pre-wrap break-words">
          {{ value(field.current) }}
        </dd></template
      >
      <dt class="inbox-caption">
        {{
          t(
            preview.manual_attestation
              ? 'inboxInvestigation.investigatedValue'
              : 'inboxInvestigation.proposedValue',
          )
        }}
      </dt>
      <dd class="max-h-40 overflow-auto whitespace-pre-wrap break-words">
        {{ value(field.proposed) }}
      </dd>
    </dl>
    <p class="inbox-caption">{{ t('inboxInvestigation.remainingReview') }}</p>
  </div>
</template>
