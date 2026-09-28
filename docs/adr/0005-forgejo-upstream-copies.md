# 0005. Forgejo upstream copies: a sync bot plus a pre-receive hook

Date: 2026-09-28
Status: Accepted

## Context

Consumers keep some repositories upstream (for example public projects on
GitHub) and want a Forgejo copy that stays current *and* can carry private
branches under a prefix such as `internal/`. The rest of the copy must stay
exactly as upstream has it: nobody may push `main` or a feature branch on
Forgejo and so fork history silently.

Two built-in Forgejo features look like they fit. Verified on Forgejo 16.0.5,
neither does:

- **Pull mirrors** keep a copy current, but the whole repository is
  read-only. No one can push, so private branches are impossible.
- **Branch protection** can't express "everything except `internal/**`":
  - a rule named `*` matches only names without a slash (`main` yes,
    `feature/x` no);
  - a `**` rule takes precedence over a more specific `internal/**` rule, in
    either creation order, so a developer is refused on `internal/x` too;
  - Forgejo 16 has no rule priorities.

## Decision

An upstream copy is an ordinary repository with three parts:

1. **A sync job** (a systemd timer on the Forgejo host) fetches upstream and
   pushes changed refs to Forgejo as a dedicated local bot account.
   - The default branch and tags are pushed without force (fast-forward only).
   - Other branches are force-pushed to follow upstream exactly.
   - Deletions are copied.
   - The private prefix is excluded both when fetching and when comparing.
2. **A `pre-receive` hook** in the repository's `hooks/pre-receive.d`, which
   Forgejo 16's central hook still runs, decides by `GITEA_PUSHER_NAME`:
   - the bot may move anything except the private prefix;
   - everyone else may move only the private prefix.
3. **Provisioning** creates the bot and its token, the repository, the bot's
   write access and the hook, and refuses to adopt a pull mirror.

## Consequences

- **Private branches coexist with an exact copy of upstream,** and web-UI
  merges are covered by the same hook, because they are pushes too.
- **History is never rewritten silently.** Fast-forward-only on the default
  branch and tags means an upstream rewrite needs a human decision; the
  runbook describes it.
- **It relies on per-repository custom hooks,** which a future Forgejo could
  change. The hook is plain POSIX `sh` and small; if hooks move again, the
  install location is the only thing to adapt.
- **The bot is a real account with write access to the copies.** Its token is
  kept outside the backups and re-issued automatically.
- **Pushing work upstream stays a manual, deliberate step.**
