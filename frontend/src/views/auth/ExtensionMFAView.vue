<script setup lang="ts">
import { onMounted, onBeforeUnmount, ref } from 'vue'
import { api, AuthenticationRefreshedError } from '@/services/api'

const status = ref<{enabled:boolean;verified:boolean;generation:number} | null>(null)
const busy = ref(false)
const error = ref('')
const notice = ref('')
const enrollment = ref<{id:string;secret:string} | null>(null)
const code = ref('')
const recovery = ref(false)
const recoveryCodes = ref<string[]>([])
const prefix = '/extensions/security/mfa'
async function load() {
  status.value = (await api.get(prefix)).data.data
}
async function run(operation: () => Promise<void>) {
  busy.value = true; error.value = ''; notice.value = ''
  try { await operation() } catch (cause) {
    error.value = 'Security operation failed. Check your code and permissions, then try again.'
    if (cause instanceof AuthenticationRefreshedError && cause.canReconcile) {
      try {
        // This is a new read under the current session, never a code resubmission.
        // A verified status describes the session, not success of the lost operation.
        await load()
        error.value = ''
        notice.value = status.value?.verified
          ? 'Current session is MFA verified.'
          : 'Session refreshed. Enter a new authenticator code.'
      } catch { /* Keep the failure visible; do not retry a one-time operation. */ }
    }
  }
  finally { busy.value = false; code.value = '' }
}
async function begin() {
  await run(async () => { enrollment.value = (await api.post(`${prefix}/enroll`, {})).data.data; recoveryCodes.value = [] })
}
async function verify() {
  await run(async () => {
    if (enrollment.value) {
      const result = await api.post(`${prefix}/confirm`, {enrollment_id:enrollment.value.id,code:code.value})
      enrollment.value = null
      recoveryCodes.value = result.data.data.recovery_codes
      notice.value = 'Authenticator enabled. Save your recovery codes before leaving this page.'
    } else {
      await api.post(`${prefix}/challenge`, {code:code.value,recovery:recovery.value})
      notice.value = 'MFA verified for this session.'
    }
    await load()
  })
}
async function revoke() {
  await run(async () => {
    await api.post(`${prefix}/revoke`, {generation:status.value?.generation})
    enrollment.value = null; recoveryCodes.value = []
    await load()
    notice.value = 'Factor revoked. Existing MFA sessions and emergency grants are invalid. Contact your security administrator for recovery.'
  })
}
onMounted(() => run(load))
onBeforeUnmount(() => { enrollment.value = null; code.value = ''; recoveryCodes.value = [] })
</script>

<template>
  <main class="max-w-2xl mx-auto p-6 space-y-6">
    <h1 class="text-2xl font-semibold">Multi-factor authentication</h1>
    <p>Protected extensions require your password or SSO session and a current authenticator or recovery code. Permissions are checked by the server.</p>
    <p v-if="error" role="alert" class="text-destructive">{{ error }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <section v-if="status" class="space-y-4">
      <p>Authenticator: {{ status.enabled ? 'Enabled' : 'Not enabled' }}. This session: {{ status.verified ? 'MFA verified' : 'MFA required for protected access' }}.</p>
      <button v-if="!status.enabled || status.verified" data-action="enroll" class="border rounded px-4 py-2" :disabled="busy" @click="begin">{{ status.enabled ? 'Replace authenticator' : 'Set up authenticator' }}</button>
      <section v-if="enrollment" class="space-y-3">
        <h2 class="font-semibold">Add an authenticator account</h2>
        <p>Enter this setup key into your authenticator using time based codes, six digits, SHA-1 and a 30 second interval. Keep this key private; it is shown only on this page.</p>
        <pre class="p-3 border rounded break-all whitespace-pre-wrap">{{ enrollment.secret }}</pre>
      </section>
      <form v-if="enrollment || status.enabled" class="space-y-3" @submit.prevent="verify">
        <label for="mfa-code">{{ recovery && !enrollment ? 'Recovery code' : 'Authenticator code' }}</label>
        <input id="mfa-code" v-model="code" name="code" class="block border rounded px-3 py-2" type="text" autocomplete="one-time-code" :inputmode="recovery && !enrollment ? 'text' : 'numeric'" :maxlength="recovery && !enrollment ? 32 : 6" required :disabled="busy">
        <label v-if="!enrollment" class="block"><input v-model="recovery" type="checkbox" :disabled="busy"> Use a recovery code</label>
        <button type="submit" class="border rounded px-4 py-2" :disabled="busy || !code">{{ enrollment ? 'Confirm enrollment' : 'Verify MFA' }}</button>
      </form>
      <section v-if="recoveryCodes.length" class="space-y-3">
        <h2 class="font-semibold">Save your one-time recovery codes</h2>
        <p>Store these offline in a safe place. Each code works once. They cannot be retrieved again.</p>
        <pre class="border rounded p-3">{{ recoveryCodes.join('\n') }}</pre>
        <button data-action="acknowledge" class="border rounded px-4 py-2" @click="recoveryCodes = []">I saved my recovery codes</button>
      </section>
      <section v-if="status.enabled && status.verified" class="space-y-3 border-t pt-4">
        <p>Revoking disables your factor, invalidates MFA sessions and revokes emergency grants. Recovery requires your security administrator.</p>
        <button class="border rounded px-4 py-2 text-destructive" :disabled="busy" @click="revoke">Revoke this factor</button>
      </section>
    </section>
  </main>
</template>
