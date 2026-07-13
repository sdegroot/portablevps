# Reusable blue-green (zero-downtime) deploy machinery for a stateless
# container app fronted by the portablevps proxy.
#
# An app module calls this helper and merges the returned config fragment. It
# produces, for one logical service `name`:
#   - two colour quadlets (`<name>-blue` on port+1, `<name>-green` on port+2),
#     rendered by the caller's `mkContainerText` (so image/env/auth/healthcheck
#     stay app-specific), deliberately WITHOUT [Install];
#   - a reconcile oneshot `<name>-bluegreen` that `nixos-rebuild switch` re-runs
#     via `restartTriggers = [ image ]` (so a `portablevps server deploy`
#     triggers it and the switch waits for its exit status): at boot it starts
#     the active colour; on an image change it warms the idle colour on the new
#     image, waits for HTTP health, flips the active-colour state file, then
#     drains the old colour;
#   - the proxy backends (both colour ports + a health check) via mkDefault, so
#     the server def only needs to set domain/visibility;
#   - a contribution to `portablevps.podman.bluegreenExcludedUnits` so the
#     generic quadlet restarter leaves the colour units to the reconcile.
#
# The single running colour is the source of truth; Traefik's loadBalancer
# healthCheck (+ the retry middleware the proxy adds for health-checked
# services) routes only to a healthy backend, so a flip drops no request.
#
# Usage (from an app module with `config`, `lib`, `pkgs` in scope):
#   bg = import ../../lib/blue-green.nix { inherit lib pkgs; };
#   ...
#   (lib.mkIf cfg.blueGreen.enable (bg {
#     inherit config;
#     name = cfg.containerName;
#     image = cfg.image;
#     port = cfg.port;
#     pullAuthFile = if useAuth then authFile else null;
#     mkContainerText = { color, containerName, port }: '' ...quadlet text... '';
#   }))
{ lib, pkgs }:

{ config
, name
, image
, port
, mkContainerText
, pullAuthFile ? null
, healthCheck ? { path = "/"; interval = "3s"; timeout = "2s"; }
, description ? "Blue-green reconcile/flip for ${name}"
}:

let
  bluePort = port + 1;
  greenPort = port + 2;

  # Run on the box by the switch (restartTriggers on the image) and at boot
  # (wantedBy apps.target). Values are inlined by nix.
  reconcileScript = ''
    set -u
    name=${name}
    bluePort=${toString bluePort}
    greenPort=${toString greenPort}
    targetImage=${image}
    stateDir=/var/lib/portablevps/$name
    stateFile=$stateDir/active-color
    mkdir -p "$stateDir"

    # Pick up the colour .container definitions written by this switch (a pure
    # image bump can leave restartChangedQuadlets' change set empty).
    systemctl daemon-reload

    active=$(cat "$stateFile" 2>/dev/null || echo blue)
    case "$active" in blue|green) ;; *) active=blue ;; esac
    if [ "$active" = blue ]; then idle=green; activePort=$bluePort; idlePort=$greenPort
    else idle=blue; activePort=$greenPort; idlePort=$bluePort; fi
    activeCtr=$name-$active
    idleCtr=$name-$idle
    activeUnit=$name-$active.service
    idleUnit=$name-$idle.service

    running() { [ "$(podman inspect -f '{{.State.Running}}' "$1" 2>/dev/null || echo false)" = true ]; }
    imageof() { podman inspect -f '{{.Config.Image}}' "$1" 2>/dev/null || true; }
    # Healthy = podman health "healthy", or (image has no HEALTHCHECK) a running
    # container that answers HTTP on its port.
    healthy_ok() {
      local ctr=$1 port=$2 st
      st=$(podman inspect -f '{{.State.Health.Status}}' "$ctr" 2>/dev/null || true)
      if [ "$st" = healthy ]; then return 0; fi
      if [ -z "$st" ] || [ "$st" = "<no value>" ]; then
        if running "$ctr" && curl -fsS -o /dev/null "http://127.0.0.1:$port/"; then return 0; fi
      fi
      return 1
    }
    wait_healthy() {
      local ctr=$1 port=$2 n=0
      while [ "$n" -lt 60 ]; do
        if healthy_ok "$ctr" "$port"; then return 0; fi
        n=$((n + 1)); sleep 2
      done
      return 1
    }

    # Steady state / idempotent re-run: active already on the target image.
    if running "$activeCtr" && [ "$(imageof "$activeCtr")" = "$targetImage" ]; then
      exit 0
    fi

    # Boot / first enable: active not running -> start it (nothing to drain).
    if ! running "$activeCtr"; then
      echo "blue-green($name): starting active colour $active"
      systemctl start "$activeUnit" || true
      wait_healthy "$activeCtr" "$activePort" \
        || echo "blue-green($name): active colour $active not healthy yet; Restart=always will retry" >&2
      exit 0
    fi

    # Version change: active runs an older image -> warm idle, flip, drain.
    echo "blue-green($name): flipping $active -> $idle onto $targetImage"
    podman pull ${lib.optionalString (pullAuthFile != null) "--authfile=${pullAuthFile} "}"$targetImage" || true
    systemctl restart "$idleUnit"
    if wait_healthy "$idleCtr" "$idlePort"; then
      printf '%s' "$idle" > "$stateFile"
      # Draining the old colour SIGKILLs a container that ignores SIGTERM
      # (exit 137), which leaves the unit "failed" and makes the whole switch
      # report failure. reset-failed clears it — the stop was intentional.
      systemctl stop "$activeUnit" || true
      systemctl reset-failed "$activeUnit" 2>/dev/null || true
      echo "blue-green($name): $idle healthy and now active; drained $active"
      exit 0
    fi
    echo "blue-green($name): idle colour $idle failed health check; kept $active on old image" >&2
    systemctl stop "$idleUnit" || true
    systemctl reset-failed "$idleUnit" 2>/dev/null || true
    exit 1
  '';
in
{
  environment.etc."containers/systemd/${name}-blue.container".text =
    mkContainerText { color = "blue"; containerName = "${name}-blue"; port = bluePort; };
  environment.etc."containers/systemd/${name}-green.container".text =
    mkContainerText { color = "green"; containerName = "${name}-green"; port = greenPort; };

  systemd.services."${name}-bluegreen" = {
    inherit description;
    after = [ "network-online.target" ];
    wants = [ "network-online.target" ];
    partOf = [ "apps.target" ];
    wantedBy = lib.optional (!config.portablevps.restoreMode) "apps.target";
    # Re-run by `nixos-rebuild switch` whenever the image changes; the switch
    # waits for this oneshot and propagates its exit status to the deploy.
    restartTriggers = [ image ];
    path = with pkgs; [ podman curl coreutils systemd ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
    };
    script = reconcileScript;
  };

  # Front both colour ports; Traefik's health check routes only to the running
  # (active) colour. Ports live here (single source); the server def sets
  # domain/visibility on the same service (submodule merge).
  portablevps.proxy.http.services.${name} = {
    upstreams = lib.mkDefault [
      "http://127.0.0.1:${toString bluePort}"
      "http://127.0.0.1:${toString greenPort}"
    ];
    healthCheck = lib.mkDefault healthCheck;
  };

  # The colour units are lifecycle-managed by the reconcile oneshot, not the
  # generic quadlet restarter.
  portablevps.podman.bluegreenExcludedUnits = [ "${name}-blue" "${name}-green" ];
}
