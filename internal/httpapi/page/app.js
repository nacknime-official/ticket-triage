// The flow of PRD §3.4: POST /tickets, keep the access token in memory
// only, trade it for a single-use stream token, follow the status over
// EventSource until a final status. Tokens never go to storage, cookies or
// the page URL; the stream token is in the EventSource URL only, and the
// server's Referrer-Policy keeps it from leaking.
"use strict";

(() => {
  const finalStatuses = new Set(["classified", "needs_human"]);
  const statusNames = {
    pending: "waiting for classification",
    classified: "handed to a team",
    needs_human: "handed to a person",
  };
  // Consecutive failed attempts to (re)open the stream before giving up.
  const maxStreamFailures = 6;
  const maxBackoffSeconds = 30;

  const $ = (id) => document.getElementById(id);
  const form = $("ticket-form");
  const subject = $("subject");
  const body = $("body");
  const formError = $("form-error");
  const submit = $("submit");
  const result = $("result");
  const statusName = $("status-name");
  const statusMessage = $("status-message");
  const streamNote = $("stream-note");
  const again = $("again");

  // The ticket being followed: {id, accessToken, source, timer, failures, done}.
  let current = null;

  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    showFormError("");
    if (subject.value.trim() === "" || body.value.trim() === "") {
      showFormError("Please fill in both the subject and the message.");
      return;
    }
    submit.disabled = true;
    try {
      await submitTicket(subject.value, body.value);
    } finally {
      submit.disabled = false;
    }
  });

  again.addEventListener("click", () => {
    stop(current);
    current = null;
    form.reset();
    result.hidden = true;
    form.hidden = false;
    subject.focus();
  });

  async function submitTicket(subjectText, bodyText) {
    let resp;
    try {
      resp = await fetch("/tickets", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ subject: subjectText, body: bodyText }),
        cache: "no-store",
      });
    } catch {
      showFormError("Could not reach the server. Check your connection and try again.");
      return;
    }
    if (resp.status !== 202) {
      showFormError(await submitErrorText(resp));
      return;
    }
    let t;
    try {
      t = await resp.json();
    } catch {
      showFormError("The server sent an unexpected answer. Please try again.");
      return;
    }
    current = { id: t.id, accessToken: t.access_token, source: null, timer: 0, failures: 0, done: false };
    form.hidden = true;
    result.hidden = false;
    result.classList.remove("final");
    again.hidden = true;
    showStatus({ status: t.status, message: "Your request was received and is waiting to be classified." });
    connect(current);
  }

  async function submitErrorText(resp) {
    switch (resp.status) {
      case 400: {
        const detail = await errorMessage(resp);
        return "Please check the form" + (detail ? ": " + detail + "." : ".");
      }
      case 413:
        return "The request is too large. Please shorten the message and try again.";
      case 415:
        return "The server did not accept the request format. Please reload the page.";
      case 429:
        return "Too many requests from your address. Please try again " + retryText(resp) + ".";
      case 503:
        return "The service is busy right now. Please try again " + retryText(resp) + ".";
      default:
        return "Something went wrong (HTTP " + resp.status + "). Please try again later.";
    }
  }

  // errorMessage returns the message of the JSON error body, or "".
  async function errorMessage(resp) {
    try {
      const b = await resp.json();
      return (b && b.error && typeof b.error.message === "string") ? b.error.message : "";
    } catch {
      return "";
    }
  }

  function retryAfterSeconds(resp) {
    const n = Number.parseInt(resp.headers.get("Retry-After") || "", 10);
    return Number.isFinite(n) && n >= 0 ? n : null;
  }

  function retryText(resp) {
    const s = retryAfterSeconds(resp);
    if (s === null) return "later";
    if (s < 90) return "in " + Math.max(s, 1) + (s === 1 ? " second" : " seconds");
    return "in about " + Math.round(s / 60) + " minutes";
  }

  // connect mints a stream token and opens the event stream. The stream
  // token is single-use, so every reconnect mints a new one (PRD §3.4).
  async function connect(c) {
    if (c.done) return;
    let resp;
    try {
      resp = await fetch("/tickets/" + encodeURIComponent(c.id) + "/stream-token", {
        method: "POST",
        headers: { Authorization: "Bearer " + c.accessToken },
        cache: "no-store",
      });
    } catch {
      retryLater(c, null);
      return;
    }
    if (c.done) return;
    if (resp.status === 404) {
      giveUp(c, "Status updates are not available for this request.");
      return;
    }
    if (resp.status !== 201) {
      retryLater(c, resp.status === 429 ? retryAfterSeconds(resp) : null);
      return;
    }
    let streamToken;
    try {
      streamToken = (await resp.json()).stream_token;
    } catch {
      retryLater(c, null);
      return;
    }
    if (c.done) return;
    open(c, streamToken);
  }

  function open(c, streamToken) {
    const es = new EventSource("/tickets/" + encodeURIComponent(c.id) +
      "/events?token=" + encodeURIComponent(streamToken));
    c.source = es;
    es.addEventListener("status", (ev) => {
      let t;
      try {
        t = JSON.parse(ev.data);
      } catch {
        return;
      }
      c.failures = 0;
      streamNote.hidden = true;
      showStatus(t);
      if (finalStatuses.has(t.status)) {
        stop(c);
        result.classList.add("final");
        again.hidden = false;
      }
    });
    // The browser's own reconnect would reuse the spent stream token and get
    // a 404, so close and reconnect with a new one instead.
    es.addEventListener("error", () => {
      es.close();
      c.source = null;
      if (!c.done) retryLater(c, null);
    });
  }

  function retryLater(c, seconds) {
    c.failures++;
    if (c.failures > maxStreamFailures) {
      giveUp(c, "Lost the connection to status updates. Your request is saved and will still be handled.");
      return;
    }
    const delay = seconds !== null ? seconds : Math.min(2 ** (c.failures - 1), maxBackoffSeconds);
    showNote("Reconnecting to status updates…");
    c.timer = setTimeout(() => connect(c), delay * 1000);
  }

  function giveUp(c, text) {
    stop(c);
    showNote(text);
    again.hidden = false;
  }

  // stop ends following c and drops its tokens.
  function stop(c) {
    if (!c) return;
    c.done = true;
    c.accessToken = "";
    clearTimeout(c.timer);
    if (c.source) c.source.close();
    c.source = null;
  }

  function showStatus(t) {
    statusName.textContent = statusNames[t.status] || t.status;
    statusMessage.textContent = t.message || "";
  }

  function showNote(text) {
    streamNote.textContent = text;
    streamNote.hidden = false;
  }

  function showFormError(text) {
    formError.textContent = text;
    formError.hidden = text === "";
  }
})();
