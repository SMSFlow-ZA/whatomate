<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, useId, watch } from 'vue'
import '@/assets/inbox.css'
import { useI18n } from 'vue-i18n'
import { Braces, Check } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import { inboxService, type InboxMessageVariable } from '@/services/inbox'
import { useAuthStore } from '@/stores/auth'

defineProps<{ disabled?: boolean }>()
const emit = defineEmits<{ insert: [token: string] }>()
const { t } = useI18n()
const auth = useAuthStore()
const open = ref(false),
  busy = ref(false),
  failed = ref(false)
const query = ref(''),
  selected = ref('')
const variables = ref<InboxMessageVariable[]>([])
const search = ref<HTMLInputElement>()
const results = ref<HTMLElement>()
const listId = useId()
const selectedToken = computed(() => '{{' + selected.value + '}}')
const selectedId = computed(
  () => `${listId}-${visible.value.findIndex((v) => v.key === selected.value)}`,
)
let generation = 0
let inserted = false
const visible = computed(() =>
  variables.value.filter(
    (variable) =>
      !variable.archived &&
      `${variable.label} ${variable.key}`
        .toLocaleLowerCase()
        .includes(query.value.toLocaleLowerCase()),
  ),
)
watch(visible, (options) => {
  if (!options.some((option) => option.key === selected.value))
    selected.value = options[0]?.key ?? ''
})
async function load() {
  const current = ++generation,
    organization = auth.organizationId
  // Keep focus inside the popover while retry/loading controls are replaced.
  search.value?.focus()
  busy.value = true
  failed.value = false
  variables.value = []
  try {
    const { data } = await inboxService.messageVariables()
    if (
      current !== generation ||
      organization !== auth.organizationId ||
      !open.value
    )
      return
    variables.value = data.data.variables
    await nextTick()
    if (
      current === generation &&
      organization === auth.organizationId &&
      open.value
    )
      search.value?.focus()
  } catch {
    if (current === generation) failed.value = true
  } finally {
    if (current === generation) busy.value = false
  }
}
watch(open, (value) => {
  generation++
  if (value) {
    inserted = false
    query.value = ''
    selected.value = ''
    void load()
  } else busy.value = false
})
watch(
  () => [auth.organizationId, auth.user?.id],
  () => {
    generation++
    open.value = false
    variables.value = []
  },
)
onUnmounted(() => {
  generation++
})
function insert() {
  if (
    !open.value ||
    busy.value ||
    !visible.value.some((variable) => variable.key === selected.value)
  )
    return
  const token = '{{' + selected.value + '}}'
  inserted = true
  open.value = false
  emit('insert', token)
}
function navigate(event: KeyboardEvent) {
  const options = visible.value
  if (!options.length || busy.value) return
  const index = options.findIndex((v) => v.key === selected.value)
  let next = index
  if (event.key === 'ArrowDown') next = Math.min(index + 1, options.length - 1)
  else if (event.key === 'ArrowUp') next = Math.max(index - 1, 0)
  else if (event.key === 'Home') next = 0
  else if (event.key === 'End') next = options.length - 1
  else if (event.key === 'Enter' || event.key === ' ') {
    event.preventDefault()
    insert()
    return
  } else return
  event.preventDefault()
  selected.value = options[next]!.key
  void nextTick(() =>
    document
      .getElementById(selectedId.value)
      ?.scrollIntoView({ block: 'nearest' }),
  )
}
function selectVariable(key: string, confirm = false) {
  selected.value = key
  if (confirm) insert()
  else results.value?.focus()
}
function restoreFocus(event: Event) {
  // Insertion restores the authoring selection, rather than the toolbar button.
  if (inserted) event.preventDefault()
  inserted = false
}
</script>

<template>
  <Popover v-model:open="open">
    <PopoverTrigger as-child>
      <Button
        type="button"
        variant="ghost"
        size="sm"
        :disabled="disabled"
        class="inbox-variable-trigger w-fit justify-self-start"
      >
        <Braces class="mr-2 h-4 w-4" aria-hidden="true" />{{
          t('inboxPersonalisation.insert')
        }}
      </Button>
    </PopoverTrigger>
    <PopoverContent
      align="start"
      side="top"
      :collision-padding="12"
      class="inbox-ui inbox-variable-picker"
      @close-auto-focus="restoreFocus"
    >
      <p class="inbox-section-heading">
        {{ t('inboxPersonalisation.choose') }}
      </p>
      <label class="inbox-label"
        >{{ t('inboxPersonalisation.search') }}
        <input
          ref="search"
          v-model="query"
          type="search"
          :readonly="busy"
          :aria-controls="listId"
          @keydown.down.prevent="results?.focus()"
        />
      </label>
      <p v-if="busy" role="status" class="inbox-caption">
        {{ t('common.loading') }}
      </p>
      <template v-else-if="failed">
        <p role="alert" class="inbox-caption">
          {{ t('inboxPersonalisation.loadError') }}
        </p>
        <Button
          type="button"
          variant="outline"
          class="min-h-[44px]"
          @click="load"
          >{{ t('common.retry') }}</Button
        >
      </template>
      <template v-else>
        <div
          v-if="visible.length"
          class="inbox-variable-results"
          :id="listId"
          ref="results"
          role="listbox"
          tabindex="0"
          :aria-label="t('inboxPersonalisation.available')"
          :aria-activedescendant="selectedId"
          @keydown="navigate"
        >
          <div
            v-for="(variable, index) in visible"
            :key="variable.key"
            :id="`${listId}-${index}`"
            role="option"
            :aria-selected="selected === variable.key"
            class="inbox-variable-option"
            @click="selectVariable(variable.key)"
            @dblclick="selectVariable(variable.key, true)"
          >
            <Check
              class="mt-0.5 h-3.5 w-3.5 shrink-0"
              :class="{ invisible: selected !== variable.key }"
              aria-hidden="true"
            />
            <span class="min-w-0 flex-1 font-medium">{{ variable.label }}</span>
            <span class="inbox-caption">{{ variable.scope }}</span>
          </div>
        </div>
        <p v-else role="status" class="inbox-caption">
          {{ t('inboxPersonalisation.noResults') }}
        </p>
        <p v-if="selected" class="inbox-caption break-all font-mono">
          {{ selectedToken }}
        </p>
        <Button type="button" size="sm" :disabled="!selected" @click="insert">{{
          t('inboxPersonalisation.insert')
        }}</Button>
      </template>
    </PopoverContent>
  </Popover>
</template>
