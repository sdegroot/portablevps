# Helpers for writing podman Quadlet (.container) units.
#
# Quadlet reads `Environment=` with systemd's syntax: a SPACE-SEPARATED list of assignments.
# An unquoted `Environment=SCOPES=openid profile email` therefore becomes
# `--env SCOPES=openid --env profile --env email` — the value is cut at the first space and
# the remaining words leak in as variables of their own. Every assignment is written quoted,
# with the characters systemd treats specially escaped:
#   \  → \\   (escape character inside quotes)
#   "  → \"   (closes the quoted string)
#   %  → %%   (starts a systemd specifier such as %n)
{ lib }:

let
  validName = name: builtins.match "[A-Za-z_][A-Za-z0-9_]*" name != null;

  escapeValue = lib.replaceStrings [ "\\" "\"" "%" ] [ "\\\\" "\\\"" "%%" ];
in
rec {
  # One `Environment=` line for a single variable.
  environmentLine = name: value:
    let
      text = toString value;
    in
    if !validName name then
      throw "portablevps: invalid environment variable name '${name}' (use letters, digits and _, not starting with a digit)"
    else if lib.hasInfix "\n" text then
      throw "portablevps: environment variable '${name}' contains a newline, which a Quadlet Environment= line cannot hold"
    else
      "Environment=\"${name}=${escapeValue text}\"";

  # `Environment=` lines for an attribute set of variables, one per line.
  environmentLines = env: lib.concatStringsSep "\n" (lib.mapAttrsToList environmentLine env);
}
