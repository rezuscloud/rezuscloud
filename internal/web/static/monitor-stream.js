// monitor-stream.js — SSE machine lifecycle events consumer for /machines/{id}/monitor.
(function () {
  "use strict";

  const MAX_EVENTS = 1000;

  function connect() {
    const container = document.getElementById("monitor-stream");
    if (!container) return;
    const sseURL = container.dataset.sseUrl;
    if (!sseURL) return;

    const status = document.getElementById("monitor-status");
    const conn = document.getElementById("monitor-conn");
    const countEl = document.getElementById("monitor-count");
    let count = 0;
    let attempt = 0;

    function open() {
      if (status) status.textContent = "Connecting…";
      const es = new EventSource(sseURL, { withCredentials: true });
      es.onopen = function () {
        attempt = 0;
        if (status) status.textContent = "";
        if (conn) conn.textContent = "live";
      };
      es.onmessage = function (event) {
        let parsed;
        try {
          parsed = JSON.parse(event.data);
        } catch (_) {
          parsed = { type: "unknown", object: event.data };
        }
        appendEvent(container, parsed);
        count++;
        if (countEl) countEl.textContent = String(count);
      };
      es.onerror = function () {
        es.close();
        if (conn) conn.textContent = "disconnected";
        if (status) status.textContent = "Reconnecting…";
        const backoff = Math.min(1000 * Math.pow(2, attempt), 30_000);
        attempt++;
        setTimeout(open, backoff);
      };
    }
    open();
  }

  function appendEvent(container, ev) {
    const row = document.createElement("div");
    row.className = "ds-logs-line";
    const ts = document.createElement("span");
    ts.className = "ds-logs-time";
    ts.textContent = new Date().toLocaleTimeString();
    row.appendChild(ts);

    const lvl = document.createElement("span");
    lvl.className = "ds-logs-level ds-logs-level--" + typeToClass(ev.type);
    lvl.textContent = "[" + (ev.type || "event") + "]";
    row.appendChild(lvl);

    const msg = document.createElement("span");
    msg.className = "ds-logs-msg";
    msg.textContent = describeEvent(ev);
    row.appendChild(msg);

    const status = document.getElementById("monitor-status");
    container.insertBefore(row, status);

    while (container.childElementCount > MAX_EVENTS + 1) {
      const first = container.firstElementChild;
      if (first && first.id === "monitor-status") break;
      if (first) first.remove();
    }
  }

  function typeToClass(t) {
    if (t === "ADDED" || t === "MODIFIED") return "info";
    if (t === "DELETED") return "error";
    return "info";
  }

  function describeEvent(ev) {
    const obj = ev.object || {};
    const name = obj.metadata && obj.metadata.name ? obj.metadata.name : "unknown";
    return (ev.type || "event") + " " + (obj.kind || "machine") + "/" + name + " (stage=" + ((obj.status && obj.status.stage) || "?") + ")";
  }

  document.addEventListener("DOMContentLoaded", connect);
})();
