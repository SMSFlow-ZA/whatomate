package inboxlive

import "gorm.io/gorm"

// InstallRevisions installs transactional invalidation hints. PostgreSQL
// delivers NOTIFY after commit only. Resource revisions roll back with the write,
// and compatible older writers need no new application callback. No old case
// history is fabricated and no content is included in the notification payload.
func InstallRevisions(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`CREATE OR REPLACE FUNCTION whatomate_inbox_live_emit(
  namespace text, org uuid, resource_kind text, resource uuid, before_scope jsonb, after_scope jsonb)
RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  IF org IS NULL OR resource IS NULL THEN RETURN; END IF;
  -- Scope the transaction-local dirty bit to this schema. A transaction may
  -- legitimately operate on two isolated schemas during a recovery rehearsal.
  PERFORM pg_catalog.set_config('whatomate.inbox_live_'||pg_catalog.md5(namespace),'1',true);
  -- Distinct transaction keys never lock another business writer's staging row.
  -- Preserve the first old scope and the final new scope of this transaction.
  EXECUTE format('INSERT INTO %I.inbox_live_changes AS changes
    (transaction_id,organization_id,kind,resource_id,before_scope,after_scope)
    VALUES (pg_current_xact_id(),$1,$2,$3,$4,$5)
    ON CONFLICT (transaction_id,organization_id,kind,resource_id)
    DO UPDATE SET before_scope=CASE
      WHEN changes.before_scope IS NULL AND EXCLUDED.before_scope IS NULL THEN NULL
      ELSE COALESCE(EXCLUDED.before_scope,''{}''::jsonb)||COALESCE(changes.before_scope,''{}''::jsonb) END,
      after_scope=EXCLUDED.after_scope', namespace)
    USING org,resource_kind,resource,before_scope,after_scope;
END; $$`).Error; err != nil {
			return err
		}
		if err := tx.Exec(`CREATE OR REPLACE FUNCTION whatomate_inbox_live_flush()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE changed record; current_version bigint;
  signal_key text := 'whatomate.inbox_live_'||pg_catalog.md5(TG_TABLE_SCHEMA);
BEGIN
  -- Each changed row queues a deferred callback. After the first drain, avoid
  -- repeatedly scanning this transaction's deleted staging tuples (quadratic
  -- work for large imports). A later writer sets the dirty bit again.
  IF pg_catalog.current_setting(signal_key,true) IS DISTINCT FROM '1' THEN RETURN NEW; END IF;
  PERFORM pg_catalog.set_config(signal_key,'0',true);
  -- Business row changes are finished before taking revision locks. Always
  -- acquire resource keys in the same order, including multi-case transactions.
  FOR changed IN EXECUTE format('SELECT organization_id,kind,resource_id,before_scope,after_scope
    FROM %I.inbox_live_changes WHERE transaction_id=pg_current_xact_id()
    ORDER BY organization_id,kind,resource_id',TG_TABLE_SCHEMA)
  LOOP
    EXECUTE format('INSERT INTO %I.inbox_live_resources AS revision
      (organization_id,kind,resource_id,version,updated_at) VALUES ($1,$2,$3,1,clock_timestamp())
      ON CONFLICT (organization_id,kind,resource_id) DO UPDATE
      SET version=revision.version+1,updated_at=clock_timestamp() RETURNING version',TG_TABLE_SCHEMA)
      INTO current_version USING changed.organization_id,changed.kind,changed.resource_id;
    PERFORM pg_catalog.pg_notify('whatomate_inbox_live_v1',
      jsonb_build_object('v',1,'organization_id',changed.organization_id,'kind',changed.kind,
        'resource_id',changed.resource_id,'version',current_version,
        'before',changed.before_scope,'after',changed.after_scope)::text);
  END LOOP;
  -- Further deferred row invocations see no work; no staging row can survive
  -- a successful commit or appear as pending outbound work in a backup.
  EXECUTE format('DELETE FROM %I.inbox_live_changes WHERE transaction_id=pg_current_xact_id()',TG_TABLE_SCHEMA);
  RETURN NEW;
END; $$`).Error; err != nil {
			return err
		}
		if err := tx.Exec("DROP TRIGGER IF EXISTS whatomate_inbox_live_flush ON inbox_live_changes").Error; err != nil {
			return err
		}
		if err := tx.Exec("CREATE CONSTRAINT TRIGGER whatomate_inbox_live_flush AFTER INSERT OR UPDATE ON inbox_live_changes DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION whatomate_inbox_live_flush()").Error; err != nil {
			return err
		}
		if err := tx.Exec(`CREATE OR REPLACE FUNCTION whatomate_inbox_live_case_change()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  before_row jsonb := '{}'; after_row jsonb := '{}'; before_scope jsonb; after_scope jsonb;
  before_org uuid; after_org uuid; before_id uuid; after_id uuid;
BEGIN
  IF TG_OP <> 'INSERT' THEN before_row := to_jsonb(OLD); END IF;
  IF TG_OP <> 'DELETE' THEN after_row := to_jsonb(NEW); END IF;
  IF TG_OP='UPDATE' AND (before_row-'updated_at') IS NOT DISTINCT FROM (after_row-'updated_at') THEN RETURN NEW; END IF;
  before_org := (before_row->>'organization_id')::uuid;
  after_org := (after_row->>'organization_id')::uuid;
  IF TG_TABLE_NAME='inbox_cases' THEN
    before_id := (before_row->>'id')::uuid; after_id := (after_row->>'id')::uuid;
  ELSIF TG_TABLE_NAME='inbox_intake_bindings' THEN
    before_id := COALESCE(before_row->>'case_id',before_row->>'previous_case_id')::uuid;
    after_id := COALESCE(after_row->>'case_id',after_row->>'previous_case_id')::uuid;
  ELSE
    before_id := (before_row->>'case_id')::uuid; after_id := (after_row->>'case_id')::uuid;
  END IF;
  -- Only routing identifiers, never notes, field values or encrypted intake answers.
  before_scope := jsonb_strip_nulls(jsonb_build_object(
    'account_id',before_row->'account_id','contact_id',before_row->'contact_id',
    'team_id',before_row->'team_id','assignee_id',COALESCE(before_row->'assignee_id',before_row->'agent_id')));
  after_scope := jsonb_strip_nulls(jsonb_build_object(
    'account_id',after_row->'account_id','contact_id',after_row->'contact_id',
    'team_id',after_row->'team_id','assignee_id',COALESCE(after_row->'assignee_id',after_row->'agent_id')));
  IF (before_org,before_id) IS NOT DISTINCT FROM (after_org,after_id) THEN
    EXECUTE format('SELECT %I.whatomate_inbox_live_emit($1,$2,$3,$4,$5,$6)',TG_TABLE_SCHEMA)
      USING TG_TABLE_SCHEMA,after_org,'case',after_id,before_scope,after_scope;
  ELSE
    -- A moved/deleted resource does not carry another organisation's new scope.
    IF before_id IS NOT NULL THEN
      EXECUTE format('SELECT %I.whatomate_inbox_live_emit($1,$2,$3,$4,$5,$6)',TG_TABLE_SCHEMA)
        USING TG_TABLE_SCHEMA,before_org,'case',before_id,before_scope,NULL::jsonb;
    END IF;
    IF after_id IS NOT NULL THEN
      EXECUTE format('SELECT %I.whatomate_inbox_live_emit($1,$2,$3,$4,$5,$6)',TG_TABLE_SCHEMA)
        USING TG_TABLE_SCHEMA,after_org,'case',after_id,NULL::jsonb,after_scope;
    END IF;
  END IF;
  IF TG_OP='DELETE' THEN RETURN OLD; END IF;
  RETURN NEW;
END; $$`).Error; err != nil {
			return err
		}
		if err := tx.Exec(`CREATE OR REPLACE FUNCTION whatomate_inbox_live_field_change()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  before_row jsonb := '{}'; after_row jsonb := '{}';
  before_org uuid; after_org uuid; before_id uuid; after_id uuid;
  before_kind text; after_kind text;
BEGIN
  IF TG_OP <> 'INSERT' THEN before_row := to_jsonb(OLD); END IF;
  IF TG_OP <> 'DELETE' THEN after_row := to_jsonb(NEW); END IF;
  IF TG_OP='UPDATE' AND (before_row-'updated_at') IS NOT DISTINCT FROM (after_row-'updated_at') THEN RETURN NEW; END IF;
  before_org := (before_row->>'organization_id')::uuid;
  after_org := (after_row->>'organization_id')::uuid;
  IF TG_TABLE_NAME='inbox_field_definitions' THEN
    before_kind := 'configuration'; after_kind := 'configuration';
    before_id := (before_row->>'id')::uuid; after_id := (after_row->>'id')::uuid;
  ELSE
    before_id := (before_row->>'subject_id')::uuid; after_id := (after_row->>'subject_id')::uuid;
    EXECUTE format('SELECT scope FROM %I.inbox_field_definitions WHERE organization_id=$1 AND id=$2',TG_TABLE_SCHEMA)
      INTO before_kind USING before_org,(before_row->>'definition_id')::uuid;
    EXECUTE format('SELECT scope FROM %I.inbox_field_definitions WHERE organization_id=$1 AND id=$2',TG_TABLE_SCHEMA)
      INTO after_kind USING after_org,(after_row->>'definition_id')::uuid;
  END IF;
  -- A deleted legacy definition can leave an orphan value temporarily. Its
  -- definition invalidation already forces a fresh read; invent no target.
  IF (before_org,before_kind,before_id) IS NOT DISTINCT FROM (after_org,after_kind,after_id) THEN
    IF after_kind IN ('case','contact','configuration') THEN
      EXECUTE format('SELECT %I.whatomate_inbox_live_emit($1,$2,$3,$4,$5,$6)',TG_TABLE_SCHEMA)
        USING TG_TABLE_SCHEMA,after_org,after_kind,after_id,'{}'::jsonb,'{}'::jsonb;
    END IF;
  ELSE
    IF before_kind IN ('case','contact','configuration') AND before_id IS NOT NULL THEN
      EXECUTE format('SELECT %I.whatomate_inbox_live_emit($1,$2,$3,$4,$5,$6)',TG_TABLE_SCHEMA)
        USING TG_TABLE_SCHEMA,before_org,before_kind,before_id,'{}'::jsonb,NULL::jsonb;
    END IF;
    IF after_kind IN ('case','contact','configuration') AND after_id IS NOT NULL THEN
      EXECUTE format('SELECT %I.whatomate_inbox_live_emit($1,$2,$3,$4,$5,$6)',TG_TABLE_SCHEMA)
        USING TG_TABLE_SCHEMA,after_org,after_kind,after_id,NULL::jsonb,'{}'::jsonb;
    END IF;
  END IF;
  IF TG_OP='DELETE' THEN RETURN OLD; END IF;
  RETURN NEW;
END; $$`).Error; err != nil {
			return err
		}
		// Static names only. Staged/isolated schema rehearsals may not yet contain
		// optional projections; normal application migration creates all models.
		for _, table := range []string{"inbox_cases", "inbox_case_activities", "inbox_sla_obligations", "inbox_intake_bindings"} {
			if !tx.Migrator().HasTable(table) {
				continue
			}
			if err := tx.Exec("DROP TRIGGER IF EXISTS whatomate_inbox_live_change ON " + table).Error; err != nil {
				return err
			}
			if err := tx.Exec("CREATE TRIGGER whatomate_inbox_live_change AFTER INSERT OR UPDATE OR DELETE ON " + table + " FOR EACH ROW EXECUTE FUNCTION whatomate_inbox_live_case_change()").Error; err != nil {
				return err
			}
		}
		for _, table := range []string{"inbox_field_definitions", "inbox_field_values"} {
			if !tx.Migrator().HasTable(table) {
				continue
			}
			if err := tx.Exec("DROP TRIGGER IF EXISTS whatomate_inbox_live_field_change ON " + table).Error; err != nil {
				return err
			}
			if err := tx.Exec("CREATE TRIGGER whatomate_inbox_live_field_change AFTER INSERT OR UPDATE OR DELETE ON " + table + " FOR EACH ROW EXECUTE FUNCTION whatomate_inbox_live_field_change()").Error; err != nil {
				return err
			}
		}
		return nil
	})
}
