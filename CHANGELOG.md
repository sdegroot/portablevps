# Changelog

## Unreleased

- **portablevps is now a standalone repository**, extracted from the
  `epistola-nix-infra` monorepo with its own release cycle. The Nix flake
  library and Go CLI live at the repo root (no more `?dir=portablevps`);
  consume it as `github:sdegroot/portablevps`.
- **`portablevps --version` now exists.** Wired through `-ldflags`; the Nix
  build reports a git-revision-derived version, and tagged releases will
  report their real semver tag.
- **Server definitions now catch typos in `placement`/`info` immediately**,
  with a named-option error (and a "did you mean...?" suggestion) instead of
  a confusing failure deep inside unrelated module evaluation later.
- **`service migrate` now verifies the target's TLS certificate before
  touching the source**, aborting the cutover early if one never appears,
  and **automatically repoints internal NetBird DNS** and reports a
  restore-drill metric after a verified migration (previously manual
  follow-up steps only present in the legacy Python CLI).
- **No more Epistola-specific defaults in the tool itself.** The
  `epistola-suite` app (Epistola's own product, not a generically
  self-hostable one) moved to a consumer-side overlay on the generic
  `portablevps.apps.custom` schema; remaining example values and comments
  referencing `epistola.*` domains were genericized.
