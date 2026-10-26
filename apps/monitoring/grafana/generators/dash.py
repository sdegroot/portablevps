"""Shared helpers for the generated built-in dashboards (role-*.json).

Each role script (traefik.py, postgres.py, jvm.py) builds one dashboard with
these helpers and prints its JSON model. Regenerate all of them from this
directory with:

    for d in traefik postgres jvm; do python3 $d.py > ../dashboards/role-$d.json; done

Commit the script and the JSON together: Grafana provisions only the JSON.
"""
import json
import sys

VM = {"type": "victoriametrics-metrics-datasource", "uid": "victoriametrics"}
VL = {"type": "victoriametrics-logs-datasource", "uid": "victorialogs"}

# Every series a host's collector pushes carries service_name and host_name
# (OTel resource attributes, Prometheus-named by VictoriaMetrics). A role
# dashboard is always about one service, optionally narrowed to some hosts.
SEL = 'service_name="$service", host_name=~"$host"'


def color_override(name, color):
    return {"matcher": {"id": "byName", "options": name},
            "properties": [{"id": "color", "value": {"mode": "fixed", "fixedColor": color}}]}


def regex_color(pattern, color):
    return {"matcher": {"id": "byRegexp", "options": pattern},
            "properties": [{"id": "color", "value": {"mode": "fixed", "fixedColor": color}}]}


def dashed(name, color="red"):
    """A limit line (max connections, max heap): dashed and fixed-colour."""
    return {"matcher": {"id": "byName", "options": name},
            "properties": [{"id": "custom.lineStyle", "value": {"fill": "dash", "dash": [10, 10]}},
                           {"id": "custom.fillOpacity", "value": 0},
                           {"id": "color", "value": {"mode": "fixed", "fixedColor": color}}]}


def right_axis(name, unit):
    return {"matcher": {"id": "byName", "options": name},
            "properties": [{"id": "unit", "value": unit}, {"id": "custom.axisPlacement", "value": "right"}]}


def target(expr, legend="", ref="A", instant=False, fmt=None):
    t = {"refId": ref, "datasource": VM, "expr": expr, "legendFormat": legend}
    if instant:
        t["instant"] = True
        t["range"] = False
    if fmt:
        t["format"] = fmt
    return t


GREEN = [{"color": "green", "value": None}]


class Dashboard:
    """Lays panels out top to bottom: row(), then place() along a line, then advance()."""

    def __init__(self):
        self.panels = []
        self._id = 0
        self._y = 0

    def nid(self):
        self._id += 1
        return self._id

    def row(self, title, collapsed=False):
        self.panels.append({"id": self.nid(), "type": "row", "title": title, "collapsed": collapsed,
                            "gridPos": {"h": 1, "w": 24, "x": 0, "y": self._y}, "panels": []})
        self._y += 1

    def place(self, p, x, w, h):
        p["gridPos"] = {"h": h, "w": w, "x": x, "y": self._y}
        self.panels.append(p)

    def advance(self, h):
        self._y += h

    # ------------------------------------------------------------ panel kinds
    def stat(self, title, expr, unit, desc, thresholds=None, decimals=None, mappings=None,
             graph=True, color_mode="value"):
        p = {
            "id": self.nid(), "type": "stat", "title": title, "description": desc, "datasource": VM,
            "targets": [target(expr)],
            "fieldConfig": {"defaults": {
                "unit": unit,
                "color": {"mode": "thresholds"},
                "thresholds": {"mode": "absolute", "steps": thresholds or GREEN},
            }, "overrides": []},
            "options": {"reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
                        "colorMode": color_mode, "graphMode": "area" if graph else "none",
                        "textMode": "auto", "justifyMode": "auto", "orientation": "auto"},
        }
        if decimals is not None:
            p["fieldConfig"]["defaults"]["decimals"] = decimals
        if mappings:
            p["fieldConfig"]["defaults"]["mappings"] = mappings
        return p

    def ts(self, title, targets, unit, desc, stack=False, overrides=None, min0=True,
           legend_calcs=None, draw="line", max1=False):
        custom = {"drawStyle": draw, "lineWidth": 1, "fillOpacity": 10, "showPoints": "never",
                  "spanNulls": False}
        if draw == "bars":
            custom["fillOpacity"] = 70
        if stack:
            custom["stacking"] = {"mode": "normal", "group": "A"}
        defaults = {"unit": unit, "custom": custom}
        if min0:
            defaults["min"] = 0
        if max1:
            defaults["max"] = 1
        return {
            "id": self.nid(), "type": "timeseries", "title": title, "description": desc, "datasource": VM,
            "targets": targets,
            "fieldConfig": {"defaults": defaults, "overrides": overrides or []},
            "options": {"legend": {"displayMode": "table" if legend_calcs else "list", "placement": "bottom",
                                   "calcs": legend_calcs or []},
                        "tooltip": {"mode": "multi", "sort": "desc"}},
        }

    def table(self, title, targets, desc, columns, sort_by=None, overrides=None):
        """An instant table: each target is one column (format=table), joined on the labels.

        columns maps fields to display names, in order: the label columns the
        queries aggregate by, and 'Value #<ref>' per query. (Grafana names the
        value of a lone query plain 'Value'; both spellings are renamed.) The
        queries aggregate `by (...)`, so no other label columns appear.
        """
        rename = {}
        order = {}
        for i, (field, name) in enumerate(columns):
            rename[field] = name
            order[field] = i
            if field == "Value #A" and len(targets) == 1:
                rename["Value"] = name
                order["Value"] = i
        excl = {k: True for k in ("Time",)}
        return {
            "id": self.nid(), "type": "table", "title": title, "description": desc, "datasource": VM,
            "targets": targets,
            "fieldConfig": {"defaults": {"custom": {"align": "auto", "cellOptions": {"type": "auto"}}},
                            "overrides": overrides or []},
            "options": {"showHeader": True, "cellHeight": "sm", "footer": {"show": False},
                        "sortBy": [{"displayName": sort_by, "desc": True}] if sort_by else []},
            "transformations": [
                {"id": "merge", "options": {}},
                {"id": "organize", "options": {"excludeByName": excl, "indexByName": order,
                                               "renameByName": rename}},
            ],
        }

    def logs(self, title, expr, desc):
        return {
            "id": self.nid(), "type": "logs", "title": title, "description": desc, "datasource": VL,
            "targets": [{"refId": "A", "datasource": VL, "expr": expr, "queryType": "instant", "maxLines": 1000}],
            "options": {"showTime": True, "wrapLogMessage": True, "enableLogDetails": True,
                        "sortOrder": "Descending", "dedupStrategy": "none", "prettifyLogMessage": False},
        }

    def vl_range(self, title, expr, legend, unit, desc, overrides=None):
        p = self.ts(title, [{"refId": "A", "datasource": VL, "expr": expr, "queryType": "statsRange",
                             "legendFormat": legend}], unit, desc, stack=True, overrides=overrides, draw="bars")
        p["datasource"] = VL
        return p


def column_override(name, unit=None, decimals=None, thresholds=None, cell=None):
    props = []
    if unit:
        props.append({"id": "unit", "value": unit})
    if decimals is not None:
        props.append({"id": "decimals", "value": decimals})
    if thresholds:
        props.append({"id": "thresholds", "value": {"mode": "absolute", "steps": thresholds}})
        props.append({"id": "color", "value": {"mode": "thresholds"}})
    if cell:
        props.append({"id": "custom.cellOptions", "value": {"type": cell}})
    return {"matcher": {"id": "byName", "options": name}, "properties": props}


def variables(role_metric):
    """The fixed contract every role dashboard keeps: `service` and `host`.

    `service` lists only the services that ship this role's metrics (so a link
    like /d/pvps-traefik?var-service=hq works and the dropdown never offers a
    service with nothing to show). No value is pinned: Grafana selects the
    first listed service when the URL names none. `host` is narrowed to the
    chosen service and defaults to All.
    """
    return {"list": [
        {
            "name": "service", "label": "Service", "type": "query", "datasource": VM,
            "definition": f"label_values({role_metric}, service_name)",
            "query": f"label_values({role_metric}, service_name)",
            "refresh": 2, "sort": 1, "includeAll": False, "multi": False, "current": {},
        },
        {
            "name": "host", "label": "Host", "type": "query", "datasource": VM,
            "definition": f'label_values({role_metric}{{service_name="$service"}}, host_name)',
            "query": f'label_values({role_metric}{{service_name="$service"}}, host_name)',
            "refresh": 2, "sort": 1, "includeAll": True, "multi": True, "allValue": ".*",
            "current": {"selected": True, "text": ["All"], "value": ["$__all"]},
        },
    ]}


LINKS = [
    {"title": "Role dashboards", "type": "dashboards", "tags": ["role"], "asDropdown": True,
     "includeVars": True, "keepTime": True},
    {"title": "Fleet — Host Overview", "type": "link", "url": "/d/fleet-host-overview", "keepTime": True},
    {"title": "Fleet — Containers", "type": "link", "url": "/d/fleet-containers", "keepTime": True},
    {"title": "Fleet — Logs", "type": "link", "url": "/d/fleet-logs", "keepTime": True},
]


def emit(d, uid, title, description, tags, role_metric, extra_vars=None):
    templating = variables(role_metric)
    templating["list"].extend(extra_vars or [])
    model = {
        "uid": uid,
        "title": title,
        "description": description,
        "tags": ["portablevps", "role"] + tags,
        "timezone": "browser",
        "schemaVersion": 39,
        "version": 1,
        "editable": False,
        "refresh": "1m",
        "time": {"from": "now-6h", "to": "now"},
        "links": LINKS,
        "templating": templating,
        "panels": d.panels,
    }
    json.dump(model, sys.stdout, indent=2, ensure_ascii=False)
    sys.stdout.write("\n")
