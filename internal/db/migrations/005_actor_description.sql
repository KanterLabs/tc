-- Preserve agent descriptions introduced by the settings UI. The NOT NULL
-- default keeps reads deterministic for existing actors during upgrade.
ALTER TABLE actors ADD COLUMN description TEXT NOT NULL DEFAULT '';
