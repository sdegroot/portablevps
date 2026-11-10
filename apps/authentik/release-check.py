"""Compare the deployed authentik version with upstream releases and advisories.

Pushes OTLP gauges to the telemetry gateway (portablevps apps/monitoring rules
authentik-updates.yml alert on them):

  portablevps_authentik_release_check_timestamp_seconds   when this check last succeeded
  portablevps_authentik_update_available{kind,running,latest}
      kind="patch":   a newer release on the running line (2026.8.x)
      kind="feature": a newer line (2026.11 while running 2026.8)
  portablevps_authentik_unpatched_advisories{running}      how many advisories affect the running version
  portablevps_authentik_security_advisory{ghsa,severity,fixed_in,running,summary}
      one series (value 1) per published advisory that affects the running version

Which advisories affect the running version: authentik patches several release
lines at once and lists one fixed version per line ("2026.8.2", "2026.5.7",
"2026.2.7"). If the running line is listed, it is affected while it is older
than that line's fix. If it is not listed, it is affected while it is older than
every listed fix: either its line is no longer maintained, or the advisory
predates it.

Usage: release-check.py <running-version> <otlp-endpoint|-> <resource-json>
With "-" as endpoint the payload is printed instead of pushed (for testing).
On any GitHub error nothing is pushed, so the timestamp goes stale and
AuthentikReleaseCheckStale fires instead of a false all-clear.
"""

import json
import sys
import time
import urllib.request

REPO = "goauthentik/authentik"
UA = "portablevps-authentik-release-check"


def get(path):
    req = urllib.request.Request(
        f"https://api.github.com/repos/{REPO}/{path}",
        headers={"Accept": "application/vnd.github+json", "User-Agent": UA},
    )
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.load(resp)


def parse(version):
    """'2026.8.5' or 'version/2026.8.5' -> (2026, 8, 5); None for anything else (rc, junk)."""
    version = version.strip().removeprefix("version/")
    parts = version.split(".")
    if len(parts) != 3 or not all(p.isdigit() for p in parts):
        return None
    return tuple(int(p) for p in parts)


def fmt(v):
    return ".".join(str(p) for p in v)


def evaluate(running, releases, advisories):
    """Pure decision logic; returns (latest_patch, latest, affecting advisories)."""
    stable = sorted(
        {v for r in releases if not r.get("prerelease") and not r.get("draft")
         for v in [parse(r.get("tag_name", ""))] if v}
    )
    line = running[:2]
    same_line = [v for v in stable if v[:2] == line]
    latest_patch = max(same_line) if same_line else running
    latest = max(stable) if stable else running

    affecting = []
    for adv in advisories:
        if adv.get("withdrawn_at") or adv.get("state", "published") != "published":
            continue
        fixes = [v for vuln in adv.get("vulnerabilities") or []
                 for v in [parse(vuln.get("patched_versions") or "")] if v]
        if not fixes:
            continue
        line_fixes = [f for f in fixes if f[:2] == line]
        fixed_in = min(line_fixes) if line_fixes else min(fixes)
        if running < fixed_in:
            affecting.append({
                "ghsa": adv.get("ghsa_id", ""),
                "severity": adv.get("severity") or "unknown",
                "fixed_in": fmt(fixed_in),
                "summary": (adv.get("summary") or "")[:120],
            })
    return latest_patch, latest, affecting


def gauge(name, value, attrs, now_ns):
    return {"name": name, "gauge": {"dataPoints": [{
        "timeUnixNano": str(now_ns),
        "asDouble": float(value),
        "attributes": [{"key": k, "value": {"stringValue": str(v)}} for k, v in attrs.items()],
    }]}}


def main():
    running_s, endpoint, resource_json = sys.argv[1:4]
    running = parse(running_s)
    if running is None:
        print(f"cannot parse running version {running_s!r}; not checking", file=sys.stderr)
        return 1
    releases = get("releases?per_page=100")
    advisories = get("security-advisories?per_page=100")
    latest_patch, latest, affecting = evaluate(running, releases, advisories)

    now = time.time()
    now_ns = int(now * 1e9)
    r = fmt(running)
    metrics = [
        gauge("portablevps_authentik_release_check_timestamp_seconds", now, {}, now_ns),
        gauge("portablevps_authentik_update_available", int(latest_patch > running),
              {"kind": "patch", "running": r, "latest": fmt(latest_patch)}, now_ns),
        gauge("portablevps_authentik_update_available", int(latest[:2] > running[:2]),
              {"kind": "feature", "running": r, "latest": fmt(latest)}, now_ns),
        gauge("portablevps_authentik_unpatched_advisories", len(affecting), {"running": r}, now_ns),
    ] + [gauge("portablevps_authentik_security_advisory", 1, dict(a, running=r), now_ns) for a in affecting]

    payload = {"resourceMetrics": [{
        "resource": {"attributes": json.loads(resource_json)},
        "scopeMetrics": [{"metrics": metrics}],
    }]}
    print(f"running {r}, latest on line {fmt(latest_patch)}, latest {fmt(latest)}, "
          f"{len(affecting)} advisories affect it: {', '.join(a['ghsa'] for a in affecting) or 'none'}")
    if endpoint == "-":
        print(json.dumps(payload, indent=1))
        return 0
    req = urllib.request.Request(endpoint.rstrip("/") + "/v1/metrics", data=json.dumps(payload).encode(),
                                 headers={"Content-Type": "application/json"})
    urllib.request.urlopen(req, timeout=10).read()
    return 0


if __name__ == "__main__":
    sys.exit(main())
