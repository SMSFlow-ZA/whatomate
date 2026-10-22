/** Public, canonical deployment assertion. It is not location telemetry. */
export const globalAIProcessingNotice = 'This deployment is configured for application data and RAG storage in South Africa. Azure OpenAI Global Standard inference, including embedding generation, may process submitted content internationally.'
export interface AIProcessingDisclosure {
 scope:'global_standard'
 stored_data_country:'ZA'
 inference_may_be_international:true
 embeddings_may_be_international:true
 notice:string
}

export function parseAIProcessingDisclosure(raw:unknown):AIProcessingDisclosure|null {
 if (!raw || typeof raw!=='object' || Array.isArray(raw)) return null
 const value=raw as Record<string,unknown>
 const keys=['scope','stored_data_country','inference_may_be_international','embeddings_may_be_international','notice']
 if (Object.keys(value).length!==keys.length || !keys.every(key=>Object.prototype.hasOwnProperty.call(value,key))) return null
 if (value.scope!=='global_standard' || value.stored_data_country!=='ZA' || value.inference_may_be_international!==true || value.embeddings_may_be_international!==true || value.notice!==globalAIProcessingNotice) return null
 return {scope:'global_standard',stored_data_country:'ZA',inference_may_be_international:true,embeddings_may_be_international:true,notice:globalAIProcessingNotice}
}
