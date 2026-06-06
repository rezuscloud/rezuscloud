// logs-stream.js — SSE log stream consumer for /machines/{id}/logs.
//
// Connects to the SSE URL exposed by the server, appends lines into the
// .ds-logs container, supports pause / level filter / auto-scroll, and
// reconnects with exponential backoff on transient disconnects.
(function () {
  "use strict";

  const MAX_BACKOFF_MS = 30_000;
  const MAX_LINES = 5000;

  const state = {
    paused: false,
    level: "all",
    autoScroll: true,
    count: 0,
  };

  function setStatus(msg) {
    const el = document.getElementById("logs-status");
    if (el) el.textContent = msg;
  }

  function setCount(n) {
    state.count = n;
    const el = document.getElementById("logs-count");
    if (el) el.textContent = String(n);
  }

  function shouldShow(line) {
    if (state.level === "all") return true;
    if (state.level === "warn") return line.level === "warn" || line.level === "warning" || line.level === "error" || line.level === "fatal";
    if (state.level === "error") return line.level === "error" || line.level === "fatal";
    if (state.level === "info") return line.level === "info" || line.level === "debug" || line.level === "" || !line.level;
    return true;
  }

  function formatTs(iso) {
    try {
      const d = new Date(iso);
      return d.toLocaleTimeString();
    } catch (_) {
      return iso;
    }
  }

  function appendLine(container, line) {
    const row = document.createElement("div");
    row.className = "ds-logs-line";
    row.dataset.level = line.level || "";

    const ts = document.createElement("span");
    ts.className = "ds-logs-time";
    ts.textContent = formatTs(line.timestamp || line.ts || "");
    row.appendChild(ts);

    if (line.level) {
      const lvl = document.createElement("span");
      lvl.className = "ds-logs-level ds-logs-level--" + (line.level);
      lvl.textContent = "[" + line.level + "]";
      row.appendChild(lvl);
    }
    if (line.source) {
      const src = document.createElement("span");
      src.className = "ds-logs-source";
      src.textContent = line.source;
      row.appendChild(src);
    }
    const msg = document.createElement("span");
    msg.className = "ds-logs-msg";
    msg.textContent = line.message || line.msg || "";
    row.appendChild(msg);

    if (!shouldShow(line)) row.style.display = "none";

    container.insertBefore(row, document.getElementById("logs-status"));

    // Trim oldest lines beyond MAX_LINES.
    while (container.childElementCount > MAX_LINES + 1) {
      const first = container.firstElementChild;
      if (first && first.id === "logs-status") break;
      if (first) first.remove();
    }
    setCount(state.count + 1);
  }

  function scrollToBottom() {
    if (state.autoScroll) {
      window.scrollTo({ top: document.body.scrollHeight, behavior: "instant" });
    }
  }

  function applyFilter() {
    const container = document.getElementById("logs-stream");
    if (!container) return;
    container.querySelectorAll(".ds-logs-line").forEach(function (row) {
      const lvl = row.dataset.level || "";
      const line = { level: lvl };
      row.style.display = shouldShow(line) ? "" : "none";
    });
  }

  function connect() {
    const container = document.getElementById("logs-stream");
    if (!container) return;
    const sseURL = container.dataset.sseUrl;
    if (!sseURL) {
      setStatus("Live stream unavailable — falling back to polling.");
      return;
    }

    let attempt = 0;
    let es;

    function open() {
      setStatus("Connecting to live stream…");
      es = new EventSource(sseURL, { withCredentials: true });
      es.onopen = function () {
        attempt = 0;
        setStatus("Live");
      };
      es.onmessage = function (event) {
        if (state.paused) return;
        let parsed;
        try {
          parsed = JSON.parse(event.data);
        } catch (_) {
          parsed = { message: event.data };
        }
        appendLine(container, parsed);
        scrollToBottom();
      };
      es.onerror = function () {
        es.close();
        setStatus("Disconnected — reconnecting…");
        const backoff = Math.min(1000 * Math.pow(2, attempt), MAX_BACKOFF_MS);
        attempt++;
        setTimeout(open, backoff);
      };
    }
    open();
  }

  // Wire control UI once DOM is ready.
  document.addEventListener("DOMContentLoaded", function () {
    const pauseBtn = document.getElementById("logs-pause");
    if (pauseBtn) {
      pauseBtn.addEventListener("click", function () {
        state.paused = !state.paused;
        pauseBtn.textContent = state.paused ? "Resume" : "Pause";
      });
    }
    const levelSel = document.getElementById("logs-level");
    if (levelSel) {
      levelSel.addEventListener("change", function () {
        state.level = levelSel.value;
        applyFilter();
      });
    }
    const autoChk = document.getElementById("logs-autoscroll");
    if (autoChk) {
      autoChk.addEventListener("change", function () {
        state.autoScroll = autoChk.checked;
      });
    }
    connect();
  });
})();
