# Forgejo package registry and publishing from CI

Forgejo has a built-in package registry: container images (OCI), npm, Maven,
and [many more formats](https://forgejo.org/docs/latest/user/packages/). It
needs no configuration to run.

- **Ownership:** a package belongs to a user or an organization.
- **Access:** reading and publishing follow that owner's teams (unit
  `repo.packages`).

| Format | Address |
|---|---|
| Container images | `<forgejo-host>/<owner>/<image>:<tag>` |
| npm | `https://<forgejo-host>/api/packages/<owner>/npm/` |
| Maven / Gradle | `https://<forgejo-host>/api/packages/<owner>/maven` |

**People** log in with a personal access token (Settings → Applications) that
has `read:package`, or `write:package` to publish.

## Publishing from Forgejo Actions

Forgejo gives every job an automatic token, like GitHub's `GITHUB_TOKEN`, but
**it cannot publish packages**: pushes are refused (`unauthorized:
reqPackageAccess`, npm `E401`). Forgejo also doesn't support a workflow-level
`permissions: packages: write`.

portablevps therefore provides a **package publisher** per organization:

```nix
portablevps.apps.forgejo.organizations.acme = {
  # ...teams...
  packagePublisher.enable = true;
  # optional: username ? "acme-packages", secretName ? "PACKAGES_TOKEN",
  #           userVariable ? "PACKAGES_USER"
};
```

On every deploy, `forgejo-provision`:

1. **Creates a team `package-publishers`** in the org. It grants only
   `repo.packages` = write, on all of the org's repositories. It gives no
   access to code, issues or anything else.
2. **Creates the bot**, a local account (no SSO) with a random password nobody
   knows, and puts it in that team.
3. **Keeps a `write:package` token** for the bot in
   `/var/lib/portablevps/forgejo-package-publishers/<org>.token` (0600).
   - **A new token is issued only when the stored one is missing or no longer
     works.** It is validated against the org's package list.
   - **The token is not in any backup,** so a restored server issues a new one
     on its own.
4. **Writes it to the org's Actions secrets** as `PACKAGES_TOKEN`, and the bot's
   name to the org's Actions variables as `PACKAGES_USER`. Both are rewritten on
   every run, so a deleted secret comes back.

Every repository in the organization can then publish, without any setup:

```yaml
jobs:
  image:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: echo "${{ secrets.PACKAGES_TOKEN }}" | docker login forgejo.example -u "${{ vars.PACKAGES_USER }}" --password-stdin
      - run: |
          docker build -t forgejo.example/acme/app:${{ github.sha }} .
          docker push forgejo.example/acme/app:${{ github.sha }}

  npm:
    runs-on: docker
    steps:
      - uses: actions/checkout@v4
      - run: |
          npm config set @acme:registry https://forgejo.example/api/packages/acme/npm/
          npm config set -- //forgejo.example/api/packages/acme/npm/:_authToken "${{ secrets.PACKAGES_TOKEN }}"
          npm publish

  maven:
    runs-on: docker
    container: { image: maven:3-eclipse-temurin-21 }
    steps:
      - uses: actions/checkout@v4
      # settings.xml: server "forgejo" sends  Authorization: token ${{ secrets.PACKAGES_TOKEN }}
      # pom.xml: distributionManagement -> https://forgejo.example/api/packages/acme/maven
      - run: mvn -B deploy
```

What the bot can and can't do (verified against Forgejo 16.0.5):

- **It can publish and read the packages of its own organization.**
- **It can't publish to another organization** (HTTP 401).
- **It can't read the org's repositories:** the token carries only package scope.

The token *is* a long-lived secret. Forgejo's short-lived alternative,
[Authorized Integrations](https://forgejo.org/docs/latest/user/authorized-integrations/),
is configured per repository in the web UI and does not (yet) work with clients
that use basic auth, such as `docker login` (Forgejo issue #14314).

## Things to know

- **Registry pushes share the Forgejo host's disk and backups.** Old versions
  are *not* removed automatically. Set cleanup rules per owner in Forgejo's UI
  (Settings → Packages → Cleanup rules); Forgejo 16 has no API for them.
- **Hosts pulling images need a route to Forgejo** (and, on a default-deny mesh,
  a policy). They also need a read token if the owner is private.
- **Renaming an organization** keeps its packages. Clients must use the new name.
