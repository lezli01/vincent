# 146 — Several Jira projects in one binding

**Status:** ✅ done (1/1) · **Opened:** 2026-11-17

The author asked that configuring Jira take "not only one but any number of
projects … with optional jql for each". Until now a project's Jira binding
(task 142.6) named one site, one project key and one optional JQL.

Conventions for this file are in [the tasks README](README.md).

## What this changes

- **Task 142.6 decision 2's binding shape** — **widened**: `jira` carries a
  list of `{key, jql}` projects instead of one `project_key` and `jql`. The
  site, the credential and the mapping override stay one per binding.
- **Task 142 decision 6, one source per project** — **kept**: a project still
  has one provider and one Jira site; the site just syncs more than one of
  its projects.
- **Task 142.6 decision 4, the rebind** — **narrowed** by decision 4 below.
- **Task 142 decision 13.6, the sticky project id** — **kept**, per key
  (decision 3).

## Tasks

- [x] **146.1** The list end to end: migration 0056 (`jira_projects`
  replacing `jira_project_key` and `jira_jql`), `store.IssueSource.
  JiraProjects` and the selective rebind, the API's `projects` with the old
  spelling still accepted, the probe's per-project check, one combined
  listing in `issuesource.Jira` with each project's JQL held to its own
  issues, `project in (…)` in the fake's JQL subset, the CLI's repeatable
  `--jira-project KEY[:JQL]`, the TUI form's key and JQL row per project,
  the tests, the spec amendments and the guides. ✓ 2026-11-17

## Decisions

Taken 2026-11-17 by the implementer, on the author's request.

1. **One site, many projects.** The binding is `{site, projects: [{key,
   jql?}], mapping?}`: one to 50 projects, each key once, each with its own
   optional JQL held to the same rules as before (one line, no `ORDER BY`).
   The site, the credential and the mapping override are shared. *Beaten:* a
   list of whole bindings, each with its own site — that is several sources
   for one project, which task 142 decision 6 rules out, and the credential
   is one per daemon anyway.
2. **One sync, one query.** The sync keeps one `issue_sync_state` row per
   project and reads every bound project in one JQL walk: `project in (…)`
   when no project narrows, else a parenthesised OR in which each project's
   JQL is ANDed onto that project alone. Jira's issue ids are unique across a
   site, so the id cursor and the `updated` watermark walk all of them at
   once. A single project sends exactly the query it always has. *Beaten:* a
   walk and a cursor per project — a second set of sync bookkeeping, and a
   per-project status no surface has asked for.
3. **The sticky id is per key.** A single project's sync row records its id
   as before, so no existing row reads `binding_changed` after the upgrade;
   several are recorded as `KEY:id` pairs sorted by key, so any one key
   reassigned to another Jira project stops the sync as one key alone did.
4. **A rebind is a change of site or of the set of keys, and keeps what is
   still bound.** Adding a project needs an initial import of its own, which
   only a fresh sync row runs, so it is a rebind; dropping one marks only
   that project's issues `missing`. The issues of every project still named
   stay live and keep being written back. Reordering the projects, or
   editing one's JQL or the mapping, is not a rebind.
5. **Moving between bound projects is not leaving.** An issue moved from one
   bound project to another is refreshed under its new key and stays live;
   `moved` means it left every bound project. The sweep's JQL check asks each
   narrowing project about its own issues only, so one project's JQL never
   marks another's issues missing.
6. **The old spelling is still accepted.** `jira.project_key` and
   `jira.jql` in a create or PATCH body are read as a one-project list, so a
   script written for task 142.6 keeps working; sending them beside
   `projects` is refused. Responses carry `projects` only. The probe reports
   `projects: [{key, project_id}]` in place of `project_key` and
   `project_id`, checks them in order, and names the key that failed.
7. **Each surface keeps its shape.** The CLI's `--jira-project` repeats and
   takes `KEY` or `KEY:JQL` (a key holds no colon, so the first one ends it),
   and any `--jira-project` replaces the list; `--jira-jql` still narrows a
   binding's only project and is refused beside several. The TUI form shows a
   key and a JQL row per project and an "add a jira project" row; clearing a
   project's key and JQL removes it, and a refusal of `projects[i]` lands on
   that project's row.
