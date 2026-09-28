# Forgejo upstream copies

An **upstream copy** is a Forgejo repository that follows another git repository
(the *upstream*, e.g. a public project on GitHub) and can still carry
**private branches** that never leave Forgejo.

- Upstream owns every branch and tag, except branches under a **private prefix**
  (`internal/` by default).
- A timer keeps the copy in step with upstream. Nothing is ever pushed back.
- On Forgejo, people may push **only** under the private prefix. Everything
  else is moved by the sync bot alone.

Use it when the canonical repository lives elsewhere and you want an
independent copy that can be built and worked on privately. For a read-only
copy, a plain Forgejo pull mirror is simpler. For a repository whose home *is*
Forgejo, use neither.

Why not a pull mirror or branch protection: see
[ADR 0005](adr/0005-forgejo-upstream-copies.md).

## Configuration

In the server that runs the Forgejo app:

```nix
portablevps.apps.forgejo = {
  upstreamCopies = {
    "acme/widget".upstream = "https://github.com/acme/widget.git";
    "acme/gadget" = {
      upstream = "https://git.example.org/acme/gadget.git";
      privatePrefix = "local/";          # default: "internal/"
    };
  };

  upstreamSync = {
    botUsername = "upstream-sync";       # default
    interval = "5min";                   # default; a systemd time span
  };
};
```

- **The key** is the Forgejo `owner/repo`. The owner (an org or user) must
  exist; declare orgs through `organizations`, which are provisioned first.
- **`upstream`** is any git URL, read anonymously. Private upstreams are not
  supported.
- **Deploying** creates everything; there is nothing to click. See
  [Provisioning](#provisioning).

## What the sync does

`forgejo-upstream-sync.service` runs 2 minutes after boot and then every
`interval` (with up to 30 s of jitter). For each copy:

| Upstream ref | Forgejo |
|---|---|
| the default branch (upstream's `HEAD`) | fast-forward only |
| tags | fast-forward only: new tags are added, a moved tag is refused |
| any other branch | follows upstream exactly, including force-pushes and rebases |
| a ref deleted upstream | deleted |
| a branch under the private prefix | never fetched, never pushed, never deleted |

Details:

- **"Fast-forward only" protects history people may have built on.** If
  upstream rewrites its default branch or moves a tag, the sync refuses to
  follow, and the run fails loudly (see the
  [runbook](operations-runbooks.md#forgejo-upstream-copy-sync-failing)). All
  other refs of that copy are still updated.
- **Only refs that differ are pushed,** so a quiet upstream means a quiet run.
- **One failing copy does not stop the others.** The unit fails if any copy
  failed.
- **The bot's token never appears on a command line.** It reaches git through
  `GIT_CONFIG_*` environment variables.

## What people may push

A `pre-receive` hook on each copy decides by who is pushing:

| Pusher | refs under the private prefix | all other branches and tags |
|---|---|---|
| the sync bot | refused | allowed |
| anyone else, admins included | allowed | refused |

- **The refusal explains itself,** e.g. `refused: refs/heads/main is synced
  from upstream; push to refs/heads/internal/* instead`.
- **Web-UI actions go through the same hook.** Merging a pull request into
  `main` on Forgejo is refused. Pull requests *between* private branches work.
- **The bot is kept out of the private prefix too,** so a bug in the sync can
  never overwrite or delete private work.

## Working with a copy

```sh
git clone ssh://git@forgejo.example/acme/widget.git
git switch -c internal/my-experiment origin/main
git push -u origin internal/my-experiment        # allowed
git push origin main                              # refused
```

- **Keep up with upstream** with a normal `git fetch`: the copy's `main` is
  upstream's `main`, at most `interval` old.
- **Getting private work upstream** is deliberately manual. Push the branch
  under a non-private name to the upstream itself (open a PR there, for
  example). Once merged, it comes back through the sync.
- **CI on a copy:** the copy carries upstream's files, including a
  `.github/workflows` directory. **Forgejo falls back to `.github/workflows`
  when a repo has no `.forgejo/workflows`**, so either give the project a
  `.forgejo/workflows` of its own or keep the repo's Actions unit off. Pushing
  old tags can still trigger the fallback for commits that predate
  `.forgejo/`.

## Provisioning

`forgejo-provision` (run on every deploy) does, idempotently:

1. **Creates the bot**, a local account (no SSO) with a random password nobody
   knows and the address `<bot>@noreply.invalid`, if it doesn't exist.
2. **Issues the bot an API token** (scope `write:repository`) if
   `/var/lib/portablevps/forgejo-upstream-sync/token` is missing or no longer
   valid. That is the case, for example, after a restore onto a new host: the
   token is deliberately kept outside the backed-up data.
3. **Creates each missing repository**, private, with upstream's default branch.
   An existing repository is reused, **unless it is a pull mirror**:
   provisioning fails, because a mirror can't take pushes. Delete or convert
   the mirror first.
4. **Gives the bot write access** to the repository.
5. **Installs the hook** as
   `<dataRoot>/git/repositories/<owner>/<repo>.git/hooks/pre-receive.d/upstream-copy`.
   Forgejo 16's central hooks run every executable in a repository's own
   `hooks/pre-receive.d`.

The first sync after provisioning fills the new repositories.

## Where things live

| What | Where |
|---|---|
| bot token (0600) | `/var/lib/portablevps/forgejo-upstream-sync/token` |
| fetch caches (bare repos) | `/var/lib/portablevps/forgejo-upstream-sync/cache/<owner>/<repo>.git` |
| hook | `<dataRoot>/git/repositories/<owner>/<repo>.git/hooks/pre-receive.d/upstream-copy` |
| logs | `journalctl -u forgejo-upstream-sync` (and `-u forgejo-provision`) |

The caches and the token live outside Forgejo's data, so they aren't backed
up. Both are recreated on their own.

## Removing a copy

Drop it from `upstreamCopies` and deploy:

- **The sync stops,** and the timer disappears when no copies are left.
- **The repository, its branches and the bot's access stay,** and so does the
  hook. To turn the repository into an ordinary one, delete the hook file
  (see [Where things live](#where-things-live)).

## Limitations

- **Upstreams must be readable anonymously.**
- **Git LFS objects are not copied.**
- **The sync never pushes upstream,** by design.
- **Tags are immutable on the copy.** A tag moved upstream stays at its old
  commit until someone deals with it (see the runbook).
- **A private branch that upstream also creates** (e.g. an `internal/foo` on
  GitHub) is ignored: the local one wins.
