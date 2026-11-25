package inboxlive

import "gorm.io/gorm"

// All identifiers interpolated below are fixed application table names. These
// invoker functions remain schema-local for isolated recovery rehearsals.
func installContextRevisions(tx *gorm.DB) error {
	if err := tx.Exec(`CREATE OR REPLACE FUNCTION whatomate_inbox_live_contact_change()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE before_row jsonb := '{}'; after_row jsonb := '{}'; item jsonb;
  org uuid; contact uuid; account uuid; hint jsonb; phase integer;
BEGIN
  IF TG_OP <> 'INSERT' THEN before_row := to_jsonb(OLD); END IF;
  IF TG_OP <> 'DELETE' THEN after_row := to_jsonb(NEW); END IF;
  IF TG_OP='UPDATE' AND (before_row-'updated_at') IS NOT DISTINCT FROM (after_row-'updated_at') THEN RETURN NEW; END IF;
  FOR phase IN 0..1 LOOP
    item := CASE WHEN phase=0 THEN before_row ELSE after_row END;
    org := (item->>'organization_id')::uuid;
    IF org IS NULL THEN CONTINUE; END IF;
    contact := CASE WHEN TG_TABLE_NAME='contacts' THEN (item->>'id')::uuid ELSE (item->>'contact_id')::uuid END;
    IF contact IS NULL THEN CONTINUE; END IF;
    account := NULL;
    IF item->>'whats_app_account' IS NOT NULL THEN
      EXECUTE format('SELECT id FROM %I.whatsapp_accounts WHERE organization_id=$1 AND name=$2 AND deleted_at IS NULL',TG_TABLE_SCHEMA)
        INTO account USING org,item->>'whats_app_account';
    END IF;
    hint := jsonb_strip_nulls(jsonb_build_object('contact_id',contact,'account_id',account,
      'assignee_id',COALESCE(item->'assigned_user_id',item->'agent_id')));
    EXECUTE format('SELECT %I.whatomate_inbox_live_emit($1,$2,$3,$4,$5,$6)',TG_TABLE_SCHEMA)
      USING TG_TABLE_SCHEMA,org,'contact',contact,
        CASE WHEN phase=0 THEN hint ELSE NULL::jsonb END,
        CASE WHEN phase=1 THEN hint ELSE NULL::jsonb END;
  END LOOP;
  IF TG_OP='DELETE' THEN RETURN OLD; END IF;
  RETURN NEW;
END; $$`).Error; err != nil {
		return err
	}
	if err := tx.Exec(`CREATE OR REPLACE FUNCTION whatomate_inbox_live_workload_change()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE before_row jsonb := '{}'; after_row jsonb := '{}'; item jsonb;
  org uuid; account uuid; team uuid; agent uuid; resource uuid; hint jsonb;
  phase integer; account_row record; org_row record;
BEGIN
  IF TG_OP <> 'INSERT' THEN before_row := to_jsonb(OLD); END IF;
  IF TG_OP <> 'DELETE' THEN after_row := to_jsonb(NEW); END IF;
  IF TG_OP='UPDATE' AND (before_row-'updated_at') IS NOT DISTINCT FROM (after_row-'updated_at') THEN RETURN NEW; END IF;
  IF TG_TABLE_NAME='users' AND TG_OP='UPDATE' AND
    (before_row->'is_active',before_row->'is_available',before_row->'role_id',before_row->'deleted_at',before_row->'organization_id')
      IS NOT DISTINCT FROM
    (after_row->'is_active',after_row->'is_available',after_row->'role_id',after_row->'deleted_at',after_row->'organization_id') THEN RETURN NEW; END IF;
  FOR phase IN 0..1 LOOP
    item := CASE WHEN phase=0 THEN before_row ELSE after_row END;
    IF item='{}'::jsonb THEN CONTINUE; END IF;
    org := (item->>'organization_id')::uuid;
    account := (item->>'account_id')::uuid;
    team := (item->>'team_id')::uuid;
    agent := COALESCE(item->>'agent_id',item->>'user_id')::uuid;
    IF TG_TABLE_NAME='teams' THEN team := (item->>'id')::uuid; END IF;
    IF TG_TABLE_NAME='users' THEN agent := (item->>'id')::uuid; END IF;
    IF TG_TABLE_NAME='team_members' THEN
      EXECUTE format('SELECT organization_id FROM %I.teams WHERE id=$1',TG_TABLE_SCHEMA) INTO org USING team;
    END IF;
    -- A user's availability affects every membership, not just the native org.
    -- Other rows already carry an owned organisation or team relationship.
    FOR org_row IN EXECUTE CASE WHEN TG_TABLE_NAME='users' THEN
      format('SELECT $1::uuid AS id WHERE $1 IS NOT NULL UNION SELECT organization_id FROM %I.user_organizations WHERE user_id=$2',TG_TABLE_SCHEMA)
      ELSE 'SELECT $1::uuid AS id WHERE $1 IS NOT NULL' END USING org,agent
    LOOP
      FOR account_row IN EXECUTE format('SELECT id FROM %I.whatsapp_accounts WHERE organization_id=$1 AND deleted_at IS NULL AND ($2::uuid IS NULL OR id=$2)',TG_TABLE_SCHEMA)
        USING org_row.id,account
      LOOP
        hint := jsonb_strip_nulls(jsonb_build_object('account_id',account_row.id,'team_id',team,'assignee_id',agent));
        -- Stable per source/agent/team/account, never one permanent revision
        -- row for every assignment receipt or transient staging identifier.
        resource := pg_catalog.md5(TG_TABLE_NAME||':'||org_row.id::text||':'||account_row.id::text||':'||COALESCE(team::text,'')||':'||COALESCE(agent::text,''))::uuid;
        EXECUTE format('SELECT %I.whatomate_inbox_live_emit($1,$2,$3,$4,$5,$6)',TG_TABLE_SCHEMA)
          USING TG_TABLE_SCHEMA,org_row.id,'workload',resource,
            CASE WHEN phase=0 THEN hint ELSE NULL::jsonb END,
            CASE WHEN phase=1 THEN hint ELSE NULL::jsonb END;
      END LOOP;
    END LOOP;
  END LOOP;
  IF TG_OP='DELETE' THEN RETURN OLD; END IF;
  RETURN NEW;
END; $$`).Error; err != nil {
		return err
	}
	for _, table := range []string{"contacts", "conversation_notes", "messages", "agent_transfers"} {
		if !tx.Migrator().HasTable(table) || !tx.Migrator().HasTable("whatsapp_accounts") {
			continue
		}
		if err := installContextTrigger(tx, table, "contact"); err != nil {
			return err
		}
	}
	for _, table := range []string{"users", "teams", "team_members", "user_organizations", "inbox_agent_capacities", "inbox_agent_assignment_rates", "inbox_routing_configurations", "inbox_assignment_receipts"} {
		if !tx.Migrator().HasTable(table) || !tx.Migrator().HasTable("whatsapp_accounts") || !tx.Migrator().HasTable("user_organizations") {
			continue
		}
		if err := installContextTrigger(tx, table, "workload"); err != nil {
			return err
		}
	}
	return installAccessRevisions(tx)
}

func installContextTrigger(tx *gorm.DB, table, kind string) error {
	name := "whatomate_inbox_live_" + kind + "_change"
	if err := tx.Exec("DROP TRIGGER IF EXISTS " + name + " ON " + table).Error; err != nil {
		return err
	}
	return tx.Exec("CREATE TRIGGER " + name + " AFTER INSERT OR UPDATE OR DELETE ON " + table + " FOR EACH ROW EXECUTE FUNCTION " + name + "()").Error
}
