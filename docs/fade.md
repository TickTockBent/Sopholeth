# Fade: the public chat channel

Fade recreates the [original demo](https://github.com/TickTockBent/Sopholeth/tree/1fc83acf963935a2e17031077ed0a03dd4076330/web/fade)
on the current Sopholeth API and site styling. It lives at
`sopholeth.com/fade/`, inside the existing marketing deployment. There is no
new Vercel project, backend, database, account system, or Discord bridge.

Choose a lifetime, optionally add a callsign, and send a short message. Fade
turns values under `fade:v1:` into one public chat channel; unrelated keys and
invalid message payloads do not enter the conversation. The selected node's
live messages appear oldest first, with new messages at the bottom, a countdown,
and fading text. Callsigns are display labels. Anyone can read or overwrite
values, and readers can retain copies after local expiration.

The composer stays below the scrollable conversation. Enter sends; Shift+Enter
adds a new line. Composing text with an input method does not send it. Incoming
messages follow the bottom while you are caught up; scrolling up preserves your
place and reveals a button to return to new messages. Outgoing messages from
this page align right and say “you.” This is in-memory display bookkeeping,
not authenticated authorship; a changed payload at the same key loses that label.

Channel details contains node selection, optional location, and key lookup.

## Use and deployment

The marketing site's navigation links to Fade. Its static directory is
[`sites/sopholeth.com/fade`](../sites/sopholeth.com/fade/); preview the entire
marketing root using [the normal site command](../sites/README.md#local-preview),
then visit `/fade/`. The marketing deployment enables
[trailing slashes](https://vercel.com/docs/project-configuration/vercel-json#trailingslash)
so `/fade` redirects to `/fade/` before relative assets load. It imports the
parent site's color/type tokens, including
its existing font dependency. The app itself needs no build or package install.

`config.json` lists Kraid, Ridley, and Motherbrain. Automatic selection starts
at a random root and tries others after a read-side connection failure. The
selector or `?node=https://node.example` pins a single endpoint. A local HTTP
node works when the page is served over local HTTP. HTTPS pages require an
HTTPS node, and the node must allow browser CORS requests.

The node list is static site configuration, not browser-side omega verification.
`soph join` remains the client for verified public discovery. Keep the configured
origins aligned with the intended network when changing roots.

## Application protocol

Each message uses one random key, `fade:v1:<UUID>`, and this UTF-8 JSON value:

```json
{"app":"fade","schema":1,"text":"Anyone out there?","callsign":"Night owl","location":"Somewhere"}
```

`text` is 1–280 UTF-16 code units; `callsign` and `location` are required strings
that may be empty, limited to 20 and 30 code units. These match the browser's
input limits. Invalid payloads and unrelated keys are ignored. The whole
envelope fits inside a 4 KiB stream preview. Network strings are rendered as
text, never HTML. The UI trims surrounding whitespace on submission.

Send with `PUT /v1/data/<encoded-key>` and `X-TTL`. Lifetimes offered are 300,
900, 3600, 21600, and 86400 seconds; the node applies its policy. `201` means
local storage with an observed quorum, and `202` is accepted with replication
pending. The board does not claim every root has the message.

A send initiates one PUT fetch without application retries. Browsers can retry
HTTP PUTs internally after a transport failure, so this is not an exactly-once
delivery guarantee. Timeouts, interrupted connections, and
unexpected responses retain the draft and report an unknown outcome. The
generated key is put in the lookup field under Channel details before sending
so the user can inspect it. A missing value on one node cannot prove that no
other node stored it.
Read failover or node switching never retries the PUT. A failed follow-up read
does not turn an accepted write into a failed write.

The preferred read path is `/v1/stream`. A snapshot replaces the local view,
accepted writes update it, and expiration events apply only to the matching
revision. The node's clock drives countdowns. A full-value lookup uses GET and
the exposed local TTL headers; it is a later observation of that key.

If streaming is unavailable or its snapshot omits entries, Fade uses a bounded
poll every five seconds: one prefix listing of at most 80 keys and up to four
concurrent GETs. A pagination cursor produces a visible partial-view notice;
lookup can retrieve other keys. Polls do not overlap. Failed polling retries
with backoff, moving to another configured root in automatic mode. The SSE view
keeps at most 200 recent messages. These bounds keep one browser's work finite;
multiple active tabs still create real request load.

Messages and drafts stay in page memory. Reloading does not restore expired
messages from browser storage. Each replica starts its own TTL, so another
node may retain a message longer. Switching nodes rebuilds the view and clears
the previous lookup result; it does not imply a global history or backfill.

## Checks

With an existing Playwright/Chromium installation:

```bash
node test/fade/browser.cjs
```

`PLAYWRIGHT_MODULE` and `CHROMIUM_PATH` can select an existing installation, as
with the stream tests. The fixture exercises two node origins, messages and
literal HTML payloads, overwrite/expiry, direct lookup, pending and ambiguous
write outcomes, stream/poll fallback, failover, chronological chat order,
keyboard sending, scroll behavior, and the mobile composer. Set
`FADE_SCREENSHOT` to an external path for a screenshot; do not commit test images.

Live checks should use a few clearly labeled messages at the five-minute TTL.
Automated traffic generation, private rooms, and optional client encryption are
separate app work. The [roadmap](roadmap.md#demo-web-application) retains that
broader conversation-app scope.
