<script setup lang="ts">
import { ref } from 'vue'
import { simulate, logs, type DeliveryLog, type Turn } from './api'
const sender=ref(''), text=ref(''),busy=ref(false),error=ref(''),turn=ref<Turn|null>(null),deliveries=ref<DeliveryLog[]>([])
const messages=ref<{role:string;text:string;citations?:string[]}[]>([])
// Persist the exact inbound identity across uncertain HTTP results.
const pending=ref<{sender:string;text:string;id:string}|null>(null)
async function refresh(){deliveries.value=await logs(sender.value)}
async function send(){if(busy.value)return;busy.value=true;error.value='';try{if(!pending.value)pending.value={sender:sender.value,text:text.value,id:crypto.randomUUID()};const input=pending.value;const result=await simulate(input.sender,input.text,input.id);turn.value=result;messages.value.push({role:'Employee',text:input.text},{role:'Assistant',text:result.reply,citations:result.citations??[]});pending.value=null;text.value='';await refresh()}catch{error.value='The result could not be confirmed. Retry the unchanged message. Current roster eligibility and an MFA operator session with conversation:simulate are required.'}finally{busy.value=false}}
async function load(){error.value='';try{await refresh()}catch{error.value='Delivery logs are unavailable for this employee or your current access.'}}
</script>
<template>
 <main class="conversation-page">
  <header><p class="eyebrow">Development rehearsal</p><h1>Employee conversation</h1><p>Use an active synthetic roster phone. This exercises durable intake, real case references and delivery records. Carrier sending requires a separately configured trusted adapter.</p></header>
  <label>Registered synthetic phone<input v-model="sender" placeholder="+27820000001" :disabled="!!pending || busy" /></label>
  <p v-if="error" role="alert" class="error">{{error}}</p>
  <section aria-label="Conversation" class="chat"><p v-if="!messages.length" class="empty">Ask an SOP question, or send LOG CASE. Then describe the concern and CONFIRM. Use STATUS &lt;reference&gt; when several cases exist.</p><article v-for="(message,index) in messages" :key="index" :class="message.role==='Employee'?'employee':'assistant'"><strong>{{message.role}}</strong><p>{{message.text}}</p><ul v-if="message.citations?.length"><li v-for="citation in message.citations" :key="citation">{{citation}}</li></ul></article></section>
  <form @submit.prevent="send"><label>Message<textarea v-model="text" :disabled="!!pending || busy" maxlength="4000" /></label><button :disabled="busy || (!pending && (!sender || !text.trim()))">{{busy?'Processing…':pending?'Retry unchanged message':'Send rehearsal message'}}</button></form>
  <section v-if="turn"><h2>Case references</h2><p>Current step: {{turn.phase}}</p><ul><li v-for="id in turn.cases" :key="id">{{id}} <span v-if="id===turn.selected">(selected)</span></li></ul></section>
  <section><div class="logs-heading"><h2>Durable delivery log</h2><button :disabled="busy || !sender" @click="load">Refresh</button></div><p v-if="!deliveries.length">No authorized delivery records loaded.</p><table v-else><thead><tr><th>Message</th><th>State</th><th>Reference</th></tr></thead><tbody><tr v-for="entry in deliveries" :key="entry.id"><td>{{entry.text}}</td><td>{{entry.state}}</td><td>{{entry.case_id || '—'}}</td></tr></tbody></table></section>
 </main>
</template>
<style scoped>
.conversation-page{max-width:960px;margin:0 auto;padding:32px 24px;color:var(--foreground);display:grid;gap:22px}.eyebrow{font-size:12px;text-transform:uppercase;letter-spacing:.1em}h1{font-size:28px;font-weight:650}h2{font-size:18px;font-weight:600}header p{max-width:760px;line-height:1.6}label{display:grid;gap:6px;font-weight:500}input,textarea{border:1px solid var(--border,#cbd5e1);border-radius:6px;padding:10px;background:var(--background);font-weight:400}textarea{min-height:90px}button{border:1px solid var(--border,#cbd5e1);border-radius:6px;padding:8px 14px}button:disabled{opacity:.5}.chat{min-height:180px;border:1px solid var(--border,#cbd5e1);border-radius:8px;padding:16px;display:grid;gap:12px}.chat article{padding:12px;border-left:3px solid #64748b;white-space:pre-wrap}.chat .employee{margin-left:28px;border-left-color:#0891b2}.chat p{margin-top:5px}.empty{color:#64748b}form{display:grid;gap:10px}.error{color:#b91c1c}.logs-heading{display:flex;justify-content:space-between;align-items:center}table{width:100%;border-collapse:collapse;font-size:13px}th,td{padding:10px;text-align:left;border-bottom:1px solid var(--border,#cbd5e1);overflow-wrap:anywhere}td:first-child{max-width:520px}
</style>
