import { api } from '@/services/api'
export interface Turn {phase:string;reply:string;citations:string[]|null;cases:string[];selected:string}
export interface DeliveryLog {id:string;text:string;state:string;case_id?:string}
export async function simulate(sender:string,text:string,id:string) { return (await api.post<{data:Turn}>('/extensions/conversation/simulate',{sender,text,provider_message_id:id})).data.data }
export async function logs(sender:string) { return (await api.get<{data:DeliveryLog[]}>('/extensions/conversation/logs',{params:{sender}})).data.data }
