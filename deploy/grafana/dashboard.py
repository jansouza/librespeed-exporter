"""Generates deploy/grafana/dashboard.json: python3 deploy/grafana/dashboard.py > deploy/grafana/dashboard.json

Gauges are aggregated with max by (server) and counters/histograms with
sum by (server, ...), so series from replaced pods (new pod/instance labels)
merge into one per server.
"""
import json, sys

DS = {"type": "prometheus", "uid": "${datasource}"}
SEL = 'server=~"$server"'
RATE = "[$__rate_interval]"
# Min interval for panels that use RATE. A data source without a scrape
# interval makes Grafana assume 15s and resolve $__rate_interval to 1m, a
# single sample at a 60s scrape, so rate() returns nothing. 2m keeps the
# window above 2m15s: two samples at scrape intervals up to 60s.
RATE_MIN_INTERVAL = "2m"
_id = 0

def g(metric):
    return f'max by (server) ({metric}{{{SEL}}})'

def rate(metric, extra=""):
    return f'sum by (server) (rate({metric}{{{SEL}{extra}}}{RATE}))'

def target(expr, legend="", ref="A", **kw):
    t = {"datasource": DS, "expr": expr, "legendFormat": legend, "refId": ref}
    t.update(kw)
    return t

def panel(kind, title, grid, targets, unit, description="", options=None, field=None, overrides=None, **extra):
    global _id
    _id += 1
    defaults = {"unit": unit}
    defaults.update(field or {})
    p = {
        "id": _id, "type": kind, "title": title, "description": description,
        "datasource": DS, "gridPos": dict(zip("xywh", grid)), "targets": targets,
        "fieldConfig": {"defaults": defaults, "overrides": overrides or []},
    }
    if options:
        p["options"] = options
    p.update(extra)
    return p

def row(title, y):
    global _id
    _id += 1
    return {"id": _id, "type": "row", "title": title, "collapsed": False,
            "gridPos": {"x": 0, "y": y, "w": 24, "h": 1}, "panels": []}

def thresholds(*steps):
    return {"color": {"mode": "thresholds"},
            "thresholds": {"mode": "absolute", "steps": [{"color": c, "value": v} for c, v in steps]}}

stat_opts = {"reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
             "colorMode": "value", "graphMode": "none", "textMode": "value_and_name"}
# Plain values carry no status: render them in the text color instead of
# Grafana's default green/red thresholds.
neutral = {"color": {"mode": "fixed", "fixedColor": "text"}}
# Speed test results change once per interval, so draw steps with points.
step_field = {"custom": {"drawStyle": "line", "lineInterpolation": "stepAfter", "showPoints": "always",
                         "pointSize": 4, "spanNulls": True}}
line_field = {"custom": {"drawStyle": "line", "lineWidth": 1}}
status_mapping = [{"type": "value", "options": {
    "0": {"text": "Failed", "color": "red", "index": 0},
    "1": {"text": "OK", "color": "green", "index": 1}}}]
stale = thresholds(("green", None), ("orange", 7200), ("red", 10800))
stale_help = ("Turns orange after 2h and red after 3h, when the LibrespeedResultsStale alert fires "
              "(assumes the default 30m interval).")
success_rate = (f'sum by (server) (increase(librespeed_tests_total{{{SEL},result="success"}}[$__range]))'
                f' / sum by (server) (increase(librespeed_tests_total{{{SEL}}}[$__range]))')

panels = [
    # Headline numbers.
    panel("stat", "Download", (0, 0, 4, 4),
          [target(g("librespeed_download_bytes_per_second") + " * 8", "{{server}}")], "bps",
          "Last successful download measurement.", stat_opts, neutral),
    panel("stat", "Upload", (4, 0, 4, 4),
          [target(g("librespeed_upload_bytes_per_second") + " * 8", "{{server}}")], "bps",
          "Last successful upload measurement.", stat_opts, neutral),
    panel("stat", "Ping", (8, 0, 4, 4),
          [target(g("librespeed_ping_seconds"), "{{server}}")], "s",
          "Mean latency in the last successful test.", stat_opts, neutral),
    panel("stat", "Success rate", (12, 0, 4, 4),
          [target(success_rate, "{{server}}", instant=True, range=False)], "percentunit",
          "Share of speed tests that succeeded in the selected time range. Green from 99%, orange from 90%. "
          "A test that runs before the first scrape after a restart is not counted.", stat_opts,
          dict(thresholds(("red", None), ("orange", 0.9), ("green", 0.99)), decimals=1, max=1)),
    panel("stat", "Last test", (16, 0, 4, 4),
          [target(g("librespeed_up"), "{{server}}")], "none",
          "Whether the last test succeeded.", stat_opts,
          dict(thresholds(("red", None), ("green", 1)), mappings=status_mapping)),
    panel("stat", "Since last success", (20, 0, 4, 4),
          [target("time() - " + g("librespeed_last_success_timestamp_seconds"), "{{server}}")], "s",
          "Time since the last successful test. " + stale_help, stat_opts, stale),

    # The same numbers as a table, one row per server: easier to compare
    # several servers, and readable without the charts.
    panel("table", "Latest results per server", (0, 4, 24, 5),
          [target(g("librespeed_download_bytes_per_second") + " * 8", ref="A", instant=True, range=False, format="table"),
           target(g("librespeed_upload_bytes_per_second") + " * 8", ref="B", instant=True, range=False, format="table"),
           target(g("librespeed_ping_seconds"), ref="C", instant=True, range=False, format="table"),
           target(g("librespeed_jitter_seconds"), ref="D", instant=True, range=False, format="table"),
           target(g("librespeed_up"), ref="E", instant=True, range=False, format="table"),
           target("time() - " + g("librespeed_last_success_timestamp_seconds"), ref="F", instant=True, range=False, format="table"),
           # Only the labels matter; the value is always 1.
           target(f"max by (server, isp, ip) (librespeed_client_info{{{SEL}}})", ref="G", instant=True, range=False,
                  format="table")],
          "none", "Latest result of each server. " + stale_help + " ISP and public IP need collect_client_info: true; "
          "the IP is left out for servers on the local network.",
          {"showHeader": True, "cellHeight": "sm", "sortBy": [{"displayName": "Server", "desc": False}]},
          # Grafana's default 150px minimum makes 9 columns scroll on a 1280px screen.
          {"custom": {"align": "auto", "cellOptions": {"type": "auto"}, "minWidth": 100}},
          [{"matcher": {"id": "byName", "options": n}, "properties": [{"id": "unit", "value": u}]}
           for n, u in (("Download", "bps"), ("Upload", "bps"), ("Ping", "s"), ("Jitter", "s"))]
          + [{"matcher": {"id": "byName", "options": "Last test"},
              "properties": [{"id": "mappings", "value": status_mapping},
                             {"id": "custom.cellOptions", "value": {"type": "color-text"}}]},
             {"matcher": {"id": "byName", "options": "Since last success"},
              "properties": [{"id": "unit", "value": "s"}, {"id": "decimals", "value": 0},
                             {"id": "color", "value": stale["color"]},
                             {"id": "thresholds", "value": stale["thresholds"]},
                             {"id": "custom.cellOptions", "value": {"type": "color-text"}}]}]
          # ISP names run long ("AS28573 Claro NXT Telecomunicacoes Ltda"). A fixed width, unlike
          # minWidth, is taken out before the other columns share the rest, so the table does not scroll.
          + [{"matcher": {"id": "byName", "options": "ISP"}, "properties": [{"id": "custom.width", "value": 240}]}],
          transformations=[
              {"id": "merge", "options": {}},
              {"id": "organize", "options": {
                  "excludeByName": {"Time": True, "Value #G": True},
                  "renameByName": {"server": "Server", "Value #A": "Download", "Value #B": "Upload",
                                   "Value #C": "Ping", "Value #D": "Jitter", "Value #E": "Last test",
                                   "Value #F": "Since last success", "isp": "ISP", "ip": "Public IP"},
                  "indexByName": {"server": 0, "Value #A": 1, "Value #B": 2, "Value #C": 3,
                                  "Value #D": 4, "Value #E": 5, "Value #F": 6, "isp": 7, "ip": 8}}}]),

    # Download and upload get separate panels: each reads against its own
    # range, and asymmetric links (e.g. 500/50 Mbps) do not flatten upload.
    # Speeds start at zero: an auto-scaled axis turns a 5% change into a cliff.
    panel("timeseries", "Download", (0, 9, 12, 9),
          [target(g("librespeed_download_bytes_per_second") + " * 8", "{{server}}")], "bps",
          "Download speed of each successful test.", None, dict(step_field, min=0)),
    panel("timeseries", "Upload", (12, 9, 12, 9),
          [target(g("librespeed_upload_bytes_per_second") + " * 8", "{{server}}")], "bps",
          "Upload speed of each successful test.", None, dict(step_field, min=0)),
    # Ping and jitter differ by an order of magnitude; on one axis jitter
    # would sit flat at the bottom.
    panel("timeseries", "Ping", (0, 18, 12, 8),
          [target(g("librespeed_ping_seconds"), "{{server}}")], "s",
          "Mean latency of each successful test.", None, dict(step_field, min=0)),
    panel("timeseries", "Jitter", (12, 18, 12, 8),
          [target(g("librespeed_jitter_seconds"), "{{server}}")], "s",
          "Jitter of each successful test.", None, dict(step_field, min=0)),
    # One bar per calendar day (steps aligned to UTC midnight), each covering
    # the day before its timestamp.
    panel("timeseries", "Data used by tests per day", (0, 26, 24, 8),
          [target(f"sum by (server, direction) (increase(librespeed_transferred_bytes_total{{{SEL}}}[1d]))",
                  "{{server}} {{direction}}", interval="1d")], "bytes",
          "Traffic generated by the exporter's speed tests per day. Matters on metered links. Each bar covers "
          "one full UTC day, so the current day shows up after midnight UTC.", None,
          {"min": 0, "custom": {"drawStyle": "bars", "barAlignment": -1, "barWidthFactor": 0.6,
                                "fillOpacity": 80, "lineWidth": 0, "stacking": {"mode": "normal", "group": "A"}}}),

    # Stability probing (stability.enabled).
    row("Stability (stability.enabled)", 34),
    panel("timeseries", "Stability latency", (0, 35, 8, 8),
          [target(f'histogram_quantile({q}, sum by (server, le) (rate(librespeed_stability_latency_seconds_bucket{{{SEL}}}{RATE})))',
                  "{{server}} p" + str(int(q * 100)), ref)
           for q, ref in ((0.5, "A"), (0.95, "B"), (0.99, "C"))], "s",
          "Latency percentiles of the continuous stability probes. Paused during speed tests.", None, line_field,
          interval=RATE_MIN_INTERVAL),
    panel("timeseries", "Stability jitter", (8, 35, 8, 8),
          [target(rate("librespeed_stability_jitter_seconds_sum") + " / " + rate("librespeed_stability_jitter_seconds_count"),
                  "{{server}}")], "s",
          "Mean latency difference between consecutive stability probes.", None, line_field,
          interval=RATE_MIN_INTERVAL),
    panel("timeseries", "Failed stability probes", (16, 35, 8, 8),
          [target(rate("librespeed_stability_requests_total", ',result="failure"') + " / " + rate("librespeed_stability_requests_total"),
                  "{{server}}")], "percentunit",
          "Share of stability probes that failed or timed out.", None,
          {"min": 0, "custom": {"drawStyle": "line", "lineWidth": 1, "axisSoftMax": 0.1}},
          interval=RATE_MIN_INTERVAL),
    # The full latency distribution over time, one heatmap per server.
    panel("heatmap", "Stability latency distribution: $server", (0, 43, 24, 8),
          [target(f'sum by (le) (increase(librespeed_stability_latency_seconds_bucket{{{SEL}}}{RATE}))',
                  "{{le}}", format="heatmap")], "short",
          "Probes per latency bucket over time; the color scale below maps cell color to the number of probes. "
          "Spikes show up as isolated cells above the main band.",
          {"calculate": False, "yAxis": {"unit": "s", "axisPlacement": "left"},
           "rowsFrame": {"layout": "auto"},
           "color": {"mode": "scheme", "scheme": "Blues", "steps": 64, "exponent": 0.5, "fill": "dark-blue"},
           "cellGap": 1, "showValue": "never", "legend": {"show": True},
           "tooltip": {"mode": "single", "yHistogram": True, "showColorScale": True},
           "filterValues": {"le": 1e-9}},
          repeat="server", repeatDirection="v", maxPerRow=1, interval=RATE_MIN_INTERVAL),
]

dashboard = {
    "uid": "librespeed-exporter",
    "title": "LibreSpeed",
    "description": "Speed tests from librespeed-exporter",
    "tags": ["librespeed", "network"],
    "timezone": "browser",
    "editable": True,
    "schemaVersion": 39,
    "version": 1,
    "refresh": "5m",
    "time": {"from": "now-7d", "to": "now"},
    "templating": {"list": [
        {"name": "datasource", "label": "Data source", "type": "datasource", "query": "prometheus",
         "current": {}, "hide": 0, "refresh": 1},
        {"name": "server", "label": "Server", "type": "query", "datasource": DS,
         "query": {"query": "label_values(librespeed_server_info, server)", "refId": "server"},
         "definition": "label_values(librespeed_server_info, server)",
         "multi": True, "includeAll": True, "current": {}, "refresh": 2, "sort": 1, "hide": 0},
    ]},
    # Marks every time series panel where a speed test failed.
    "annotations": {"list": [{
        "name": "Failed speed tests", "datasource": DS, "enable": True, "iconColor": "red",
        "expr": f'max by (server) (librespeed_up{{{SEL}}}) == 0',
        "step": "1m", "titleFormat": "Speed test failed", "textFormat": "{{server}}",
        "tagKeys": "server", "useValueForTime": False,
    }]},
    "panels": panels,
}
json.dump(dashboard, sys.stdout, indent=2)
sys.stdout.write("\n")
