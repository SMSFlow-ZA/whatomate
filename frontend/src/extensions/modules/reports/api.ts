import { api } from '@/services/api'
export type ReportMode = 'operational' | 'board'
export interface ReportQuery { start: string; end: string; as_of?: string; zone?: string; department?: string; case_type?: string }
export interface Cell { label: string; value: number | null; suppressed: boolean }
export interface CaseReport { new_cases:number|null;resolved_created_in_period:number|null;backlog:number|null;messages:number|null;enquiries:number|null;by_zone:Record<string,number>|Cell[];by_department:Record<string,number>|Cell[];by_case_type:Record<string,number>|Cell[];partial:boolean;suppressed?:boolean }
export interface ServiceReport {inbound_received:number|null;outbound_queued:number|null;enquiries:number|null;handoffs:number|null;by_zone:Cell[];by_department:Cell[];by_case_type:Cell[];suppressed:boolean}
export interface ReportResult { service?:ServiceReport|null;report:CaseReport;metadata:{start:string;end:string;as_of:string;scope:string;timezone:string;service_metrics:{available:boolean;reason?:string}} }
export interface ReportAccess { permissions:string[];grants:{permission:string;unit_id?:string}[] }
function params(mode:ReportMode,q:ReportQuery){return mode==='board'?{start:q.start,end:q.end}:{...q}}
export async function loadReportAccess(){return (await api.get<{data:ReportAccess}>('/extensions/catalog')).data.data}
export async function loadReport(mode:ReportMode,q:ReportQuery){return (await api.get<{data:ReportResult}>(`/extensions/reports/${mode}`,{params:params(mode,q)})).data.data}
export async function downloadReport(mode:ReportMode,q:ReportQuery){return (await api.get<Blob>('/extensions/reports/export',{params:{mode,...params(mode,q)},responseType:'blob'})).data}
export function monthWindow(month:string):ReportQuery {if(!/^\d{4}-(0[1-9]|1[0-2])$/.test(month))throw new Error('Choose a valid calendar month');const [year,m]=month.split('-').map(Number) as [number,number];return {start:`${month}-01`,end:new Date(Date.UTC(year,m,1)).toISOString().slice(0,10)}}
export interface TrendQuery {end_month:string;months:number;dimension:'zone'|'department'|'case_type';zone?:string;department?:string;case_type?:string}
export interface TrendPeriod {month:string;new_cases:number;resolved_created_in_period:number;backlog:number;partial:boolean;groups:Cell[]}
export interface TrendResult {dimension:string;as_of:string;timezone:string;scope:string;periods:TrendPeriod[]}
export async function loadTrend(q:TrendQuery){return (await api.get<{data:TrendResult}>('/extensions/reports/trend',{params:{...q}})).data.data}
export async function downloadTrend(q:TrendQuery){return (await api.get<Blob>('/extensions/reports/export',{params:{mode:'trend',...q},responseType:'blob'})).data}
