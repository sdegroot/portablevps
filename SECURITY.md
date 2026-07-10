# Security policy

## Reporting a vulnerability

Please report suspected security vulnerabilities privately. Do **not** open a
public issue for a security problem.

- Use GitHub's **private vulnerability reporting** (Security → Report a
  vulnerability) on the repository, or
- email the maintainer at the address listed in the repository's contact
  metadata.

Include enough detail to reproduce: affected version/commit, configuration, and
the impact you observed. We aim to acknowledge a report within a few business
days and to agree a disclosure timeline with you.

## Scope

portablevps provisions and operates real servers, so the security-relevant
surface includes:

- the NixOS modules (firewall, SSH/mesh posture, break-glass access, proxy/ACME,
  secrets wiring);
- the CLI (`scripts/portablevps_cloud`) and its handling of provider tokens,
  age keys, and SSH;
- the templates and default option values a new consumer inherits.

Findings in third-party dependencies (nixpkgs, container images, NetBird,
restic, Traefik, etc.) are best reported upstream; tell us too if a portablevps
default meaningfully increases the exposure.

## Operator responsibilities

portablevps gives you the mechanisms; a secure deployment still depends on you:

- keep off-machine, encrypted copies of the operator age key, per-server age
  keys, the cloud-admin SSH key, and the restic repository password — losing
  these can make backups unrecoverable;
- apply an object-lock / deny-delete policy to the backup bucket so a
  compromised host cannot delete backups;
- keep `flake.lock` and pinned container images current (a stale lock is a
  security posture, not just a chore);
- restrict the mesh with default-deny access policies and scope provider and
  registry tokens to least privilege.

See `docs/operations-runbooks.md` and `docs/disaster-recovery.md` for the
operational detail behind these.

## Supported versions

Until a tagged 1.0 release, only the latest `main` is supported. Pin a specific
commit for reproducibility and update deliberately.
