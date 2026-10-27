# Extension AI retrieval fallback

Trusted instance configuration can set `[extensions].ai_retrieval_fallback` to
`ranked_lexical`. This explicitly uses at most eight relevance-ranked, current,
authorized lexical page candidates when the primary full-text query has no hits.
No embedding request is made for that fallback, including when native vectors
are configured. Operator assistance and employee conversations use the same mode.

Omit the key or set `embeddings` to retain the existing embedding fallback.
Unsupported values refuse AI provider initialization. Requests cannot choose or
change this setting. Repository/provider errors remain errors, never a trigger
to select another strategy or return unrelated pages.

This does not relax query privacy/risk screening, current publication/role checks,
source citation validation, or model-processing disclosure. It also does not
change knowledge indexing configuration or provider locality requirements.
