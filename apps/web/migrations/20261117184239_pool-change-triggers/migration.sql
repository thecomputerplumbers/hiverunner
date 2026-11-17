-- Notifications are committed atomically with access changes. Presence and
-- last_seen_at updates intentionally do not enqueue work.
--> statement-breakpoint
CREATE TRIGGER pool_change_membership_insert
AFTER INSERT ON machine_pool
BEGIN
  INSERT INTO pool_change (organization_id, revision)
  SELECT NEW.organization_id, lower(hex(randomblob(16))) WHERE true
  ON CONFLICT(organization_id) DO UPDATE SET revision = excluded.revision;
END;
--> statement-breakpoint
CREATE TRIGGER pool_change_membership_delete
AFTER DELETE ON machine_pool
BEGIN
  INSERT INTO pool_change (organization_id, revision)
  SELECT OLD.organization_id, lower(hex(randomblob(16))) WHERE true
  ON CONFLICT(organization_id) DO UPDATE SET revision = excluded.revision;
END;
--> statement-breakpoint
CREATE TRIGGER pool_change_membership_status
AFTER UPDATE OF status ON machine_pool
WHEN OLD.status IS NOT NEW.status
BEGIN
  INSERT INTO pool_change (organization_id, revision)
  SELECT NEW.organization_id, lower(hex(randomblob(16))) WHERE true
  ON CONFLICT(organization_id) DO UPDATE SET revision = excluded.revision;
END;
--> statement-breakpoint
CREATE TRIGGER pool_change_machine_status
BEFORE UPDATE OF status ON machine
WHEN OLD.status IS NOT NEW.status
BEGIN
  INSERT INTO pool_change (organization_id, revision)
  SELECT organization_id, lower(hex(randomblob(16))) FROM machine_pool WHERE machine_id = OLD.id
  ON CONFLICT(organization_id) DO UPDATE SET revision = excluded.revision;
END;
--> statement-breakpoint
CREATE TRIGGER pool_change_machine_delete
BEFORE DELETE ON machine
BEGIN
  INSERT INTO pool_change (organization_id, revision)
  SELECT organization_id, lower(hex(randomblob(16))) FROM machine_pool WHERE machine_id = OLD.id
  ON CONFLICT(organization_id) DO UPDATE SET revision = excluded.revision;
END;
--> statement-breakpoint
CREATE TRIGGER pool_change_installation_insert
AFTER INSERT ON github_installation
BEGIN
  INSERT INTO pool_change (organization_id, revision)
  SELECT NEW.organization_id, lower(hex(randomblob(16))) WHERE true
  ON CONFLICT(organization_id) DO UPDATE SET revision = excluded.revision;
END;
--> statement-breakpoint
CREATE TRIGGER pool_change_installation_delete
AFTER DELETE ON github_installation
BEGIN
  INSERT INTO pool_change (organization_id, revision)
  SELECT OLD.organization_id, lower(hex(randomblob(16))) WHERE true
  ON CONFLICT(organization_id) DO UPDATE SET revision = excluded.revision;
END;
--> statement-breakpoint
CREATE TRIGGER pool_change_installation_status
AFTER UPDATE OF status ON github_installation
WHEN OLD.status IS NOT NEW.status
BEGIN
  INSERT INTO pool_change (organization_id, revision)
  SELECT NEW.organization_id, lower(hex(randomblob(16))) WHERE true
  ON CONFLICT(organization_id) DO UPDATE SET revision = excluded.revision;
END;
--> statement-breakpoint
CREATE TRIGGER pool_change_settings_insert
AFTER INSERT ON pool_settings
BEGIN
  INSERT INTO pool_change (organization_id, revision)
  SELECT NEW.organization_id, lower(hex(randomblob(16))) WHERE true
  ON CONFLICT(organization_id) DO UPDATE SET revision = excluded.revision;
END;
--> statement-breakpoint
CREATE TRIGGER pool_change_settings_delete
AFTER DELETE ON pool_settings
BEGIN
  INSERT INTO pool_change (organization_id, revision)
  SELECT OLD.organization_id, lower(hex(randomblob(16))) WHERE true
  ON CONFLICT(organization_id) DO UPDATE SET revision = excluded.revision;
END;
--> statement-breakpoint
CREATE TRIGGER pool_change_settings_macos
AFTER UPDATE OF macos_jobs ON pool_settings
WHEN OLD.macos_jobs IS NOT NEW.macos_jobs
BEGIN
  INSERT INTO pool_change (organization_id, revision)
  SELECT NEW.organization_id, lower(hex(randomblob(16))) WHERE true
  ON CONFLICT(organization_id) DO UPDATE SET revision = excluded.revision;
END;
--> statement-breakpoint
-- Reconcile existing tunnels once when the new coordinator is deployed.
INSERT INTO pool_change (organization_id, revision)
  SELECT DISTINCT organization_id, lower(hex(randomblob(16))) FROM github_installation WHERE true
  ON CONFLICT(organization_id) DO UPDATE SET revision = excluded.revision;
