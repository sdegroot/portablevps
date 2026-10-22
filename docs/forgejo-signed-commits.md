# Forgejo: signed commits and mail

Four options of `portablevps.apps.forgejo` work together so that a
repository's default branch only takes commits whose author can be proven:

- **`requireSignedCommits`** protects every default branch with Forgejo's
  "require signed commits" rule.
- **`signing`** gives Forgejo its own SSH key, so merges and edits made in
  Forgejo are signed too.
- **`mailer`** sends mail, so an email address a user adds must be confirmed
  before it counts.
- **`commitBots`** lets automation commit to a protected branch.

Each works on its own except `requireSignedCommits`, which needs `signing`
(an assertion enforces it).

## What "verified" means to Forgejo

The rule accepts a push only if **every new commit is verified**. A commit is
verified when one of these holds:

| Signed by | Also required |
|---|---|
| a user's SSH key | the key is on that user's account **and verified there** (Settings → SSH/GPG keys → Verify), and the commit's **committer** address is active on the same account |
| a user's GPG key | the key is on the account, and the committer address is active on it |
| Forgejo's own key (`signing`) | nothing else: Forgejo only signs under the rules below |

A protection rule also refuses **force pushes** and **deletion** of the branch.
Pushing is otherwise open to everyone with write access.

## Configuration

```nix
portablevps.apps.forgejo = {
  mailer = {
    enable = true;
    host = "smtp-relay.example.com";
    from = "Code <code@example.com>";
    # usernameSecret = "forgejo/smtp-username";   # defaults; null = no auth
    # passwordSecret = "forgejo/smtp-password";
    # port = 587; protocol = "smtp+starttls";
    # notifications = true;   # mail about watched issues and PRs
    # confirmEmails = true;   # added addresses need the mailed link
  };

  signing = {
    enable = true;
    publicKey = "ssh-ed25519 AAAA... forgejo-signing";
    # privateKeySecret = "forgejo/signing-key";   # default
    name = "Code";
    email = "code@example.com";
  };

  requireSignedCommits = {
    enable = true;
    # exclude = [ "acme/legacy" ];
    # interval = "15min";
  };

  commitBots.pin-bot = {
    repositories = [ "acme/fleet" ];
    secretName = "PIN_BOT_TOKEN";   # default: COMMIT_BOT_TOKEN
  };
};
```

Create the signing key once, with no passphrase, and store the private half
in the server's sops file:

```sh
ssh-keygen -t ed25519 -N '' -C forgejo-signing -f forgejo-signing
pvps secret set <server> --key forgejo/signing-key --value-stdin < forgejo-signing
cat forgejo-signing.pub   # -> signing.publicKey
rm forgejo-signing forgejo-signing.pub
```

`forgejo-provision` fails if `publicKey` does not belong to the stored private
key: a mismatch would otherwise only show as unsigned (and refused) merges.

The key is rendered to Forgejo with a final newline added, because
`pvps secret set` strips it and OpenSSH rejects a private key without one. In
prototype mode (no secrets, e.g. a local VM) there is no private key, so
signing stays off and the build prints a warning.

A deploy restarts Forgejo when its container definition or its environment
file changes (mail settings, the SMTP password, a new `publicKey`). Rotating
the private key without changing `publicKey` is not a rotation:
`forgejo-provision` refuses the mismatch.

## `signing`: what Forgejo signs

Forgejo signs a commit it makes only when all rules of the relevant list hold:

| Commit | Option | Default | Meaning |
|---|---|---|---|
| merge, squash, rebase of a PR | `merges` | `pubkey basesigned commitssigned` | the merger has a key on their account, the target branch's head is verified, every commit being merged is verified |
| web editor, contents API | `crudActions` | `pubkey parentsigned` | the author has a key on their account, the parent commit is verified |
| a new repository's first commit | (Forgejo default) | `always` | |

Forgejo's own defaults also contain `twofa` (the person has Forgejo's own 2FA
enrolled). Accounts that sign in through an identity provider do their MFA
there and have no Forgejo 2FA, so with `twofa` no merge would ever be signed,
and every merge into a protected branch would be refused. The defaults here
leave it out.

So on a protected branch, a PR is mergeable once all its commits are verified.
A PR with an unsigned commit is refused with `won't sign: commitssigned`.

## `requireSignedCommits`

`forgejo-require-signed-commits.service` runs a minute after its timer starts
and then every `interval`. It lists every user and organization, then every
repository they own, as the break-glass admin. A repository search would miss
private repositories in organizations the admin does not own. For each
repository's default branch:

- **No rule yet:** it creates one that only requires signed commits. Pushing
  stays open to everyone with write access.
- **A rule exists:** it switches `require_signed_commits` on and leaves every
  other setting alone. Switching it off by hand does not last: the next run
  switches it back on.
- **Skipped:** archived repositories, pull mirrors, upstream copies (upstream
  owns their default branch and signs with keys Forgejo does not know), and
  `exclude`.

It never removes a rule. Taking a repository out of scope means deleting its
rule by hand as well. One failing repository does not stop the others, but the
unit fails.

## `commitBots`

Automation such as a scheduled workflow cannot simply push to a protected
branch:

- **Forgejo's automatic job token cannot push to a protected branch at all.**
  The protected-branch check looks the pusher up as a real user and fails for
  the Actions pseudo-user.
- **A commit the automation makes itself is not signed,** unless it holds a
  private key, which would then live in an Actions secret.

A commit bot avoids both. `forgejo-provision` creates the local account, gives
it write access to its repositories, keeps a `write:repository` token for it on
the host (`/var/lib/portablevps/forgejo-commit-bots`), and stores that token as
the Actions secret `secretName` on each repository. With `signing` on, the bot
also gets an SSH key whose private half is thrown away at creation. That key
only satisfies the `pubkey` rule.

The automation then works through the API with the bot's token:

1. **Create the commit** with the contents API (`POST /repos/{owner}/{repo}/contents`,
   usually with `new_branch`). Forgejo signs it; the response's
   `verification.verified` says so.
2. **Push that exact commit** to the protected branch as the bot. This is a
   fast-forward of a verified commit, so the rule accepts it.

A bot's own `git commit` is still unsigned and refused on a protected branch.

## `mailer`

Mail goes through an SMTP relay (STARTTLS on 587 by default) from `from`,
which must be a sender the relay may send for. With mail on:

- **Notifications:** users get mail about what they watch. Each user can turn
  this off in their settings. A workflow that sets
  `enable-email-notifications: true` also mails the person who triggered it
  when it fails.
- **Confirmation (`confirmEmails`):** an address a user adds in the web UI
  only becomes active through the mailed link. Without it, an added address
  is active at once, so anyone could claim any address no other account uses
  and get "verified" commits for it. Addresses added through the API get no
  mail: activate them from the web UI (Settings → Account).
- **OIDC sign-ups:** accounts created by OIDC auto-registration are active
  without a mail. The identity provider vouches for that address.

Test delivery from Site administration → Configuration → "Send test email".

## Rolling it out on an existing Forgejo

Turn the protection on last, or people get locked out of their own branches:

1. Deploy `mailer`, `signing` and any `commitBots`, then restart Forgejo.
   Check that a test email arrives.
2. Every committer: in Settings → SSH/GPG keys, add the key they sign with and
   **verify** it. Then make sure the address they commit with is active on
   their account (Settings → Account; added addresses need the mailed link).
   Check that their latest commits show as verified.
3. Move automation that pushes to a default branch onto a commit bot.
4. Enable `requireSignedCommits` and deploy.
