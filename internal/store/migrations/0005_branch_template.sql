-- 0005_branch_template: per-project branch-name template, the middle level of
-- the branch-naming chain `default < config.yaml < project < per-task literal`
-- (task 001, spec §5.3/§10).
--
-- No UNIQUE index on tasks.branch_name accompanies this, deliberately. Archived
-- tasks keep their branch_name, so an index would forbid ever reusing a name --
-- including after the user manually deleted the branch, which is the one case
-- where reuse is legitimate. The claim check is a scoped query instead.

ALTER TABLE projects ADD COLUMN branch_template TEXT;  -- NULL = inherit config.yaml
