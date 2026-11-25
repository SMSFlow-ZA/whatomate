package inboxlive

import "gorm.io/gorm"

func installAccessRevisions(tx *gorm.DB) error {
	if err := tx.Exec(`CREATE OR REPLACE FUNCTION whatomate_inbox_live_access_change()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE before_row jsonb := '{}'; after_row jsonb := '{}'; item jsonb;
  org uuid; target uuid; org_row record; phase integer; query text;
BEGIN
  IF TG_OP <> 'INSERT' THEN before_row := to_jsonb(OLD); END IF;
  IF TG_OP <> 'DELETE' THEN after_row := to_jsonb(NEW); END IF;
  IF TG_OP='UPDATE' AND (before_row-'updated_at') IS NOT DISTINCT FROM (after_row-'updated_at') THEN RETURN NEW; END IF;
  -- Password use/availability/name changes are not permission events. Session
  -- revocation and account disable remain covered by their current columns.
  IF TG_TABLE_NAME='users' AND TG_OP='UPDATE' AND
    (before_row->'role_id',before_row->'auth_stamp',before_row->'is_active',before_row->'is_super_admin',before_row->'deleted_at',before_row->'organization_id')
      IS NOT DISTINCT FROM
    (after_row->'role_id',after_row->'auth_stamp',after_row->'is_active',after_row->'is_super_admin',after_row->'deleted_at',after_row->'organization_id') THEN RETURN NEW; END IF;
  FOR phase IN 0..1 LOOP
    item := CASE WHEN phase=0 THEN before_row ELSE after_row END;
    IF item='{}'::jsonb THEN CONTINUE; END IF;
    org := (item->>'organization_id')::uuid;
    target := (item->>'id')::uuid;
    query := 'SELECT $1::uuid AS id WHERE $1 IS NOT NULL';
    IF TG_TABLE_NAME='organizations' THEN org := target;
    ELSIF TG_TABLE_NAME='users' THEN
      query := format('SELECT $1::uuid AS id WHERE $1 IS NOT NULL UNION SELECT organization_id FROM %I.user_organizations WHERE user_id=$2',TG_TABLE_SCHEMA);
    ELSIF TG_TABLE_NAME='team_members' THEN
      target := (item->>'team_id')::uuid;
      query := format('SELECT organization_id AS id FROM %I.teams WHERE id=$2',TG_TABLE_SCHEMA);
    ELSIF TG_TABLE_NAME='role_permissions' THEN
      target := (item->>'custom_role_id')::uuid;
      query := format('SELECT organization_id AS id FROM %I.custom_roles WHERE id=$2',TG_TABLE_SCHEMA);
    ELSIF TG_TABLE_NAME='permissions' THEN
      query := format('SELECT DISTINCT r.organization_id AS id FROM %I.custom_roles r JOIN %I.role_permissions rp ON rp.custom_role_id=r.id WHERE rp.permission_id=$2',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA);
    END IF;
    FOR org_row IN EXECUTE query USING org,target LOOP
      EXECUTE format('SELECT %I.whatomate_inbox_live_emit($1,$2,$3,$4,$5,$6)',TG_TABLE_SCHEMA)
        USING TG_TABLE_SCHEMA,org_row.id,'access',org_row.id,
          CASE WHEN phase=0 THEN '{}'::jsonb ELSE NULL::jsonb END,
          CASE WHEN phase=1 THEN '{}'::jsonb ELSE NULL::jsonb END;
    END LOOP;
  END LOOP;
  IF TG_OP='DELETE' THEN RETURN OLD; END IF;
  RETURN NEW;
END; $$`).Error; err != nil {
		return err
	}
	for _, table := range []string{"organizations", "users", "user_organizations", "custom_roles", "role_permissions", "permissions", "team_members", "whatsapp_accounts"} {
		if !tx.Migrator().HasTable(table) || !tx.Migrator().HasTable("user_organizations") || !tx.Migrator().HasTable("custom_roles") || !tx.Migrator().HasTable("role_permissions") {
			continue
		}
		if err := installContextTrigger(tx, table, "access"); err != nil {
			return err
		}
	}
	return nil
}
