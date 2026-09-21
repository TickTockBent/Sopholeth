# Client-side encryption with Sopholeth

Encrypt values before they reach the node when they need confidentiality.
This example uses AES-256-GCM with a random 256-bit key and a fresh 96-bit
nonce per encryption. A separate HMAC key derives an opaque storage key.
Both secrets stay with the clients.

## Local round trip

Start the private node from the [README](../README.md#run-locally).
Save the following as `encrypted-handoff.mjs` outside the repository and run
it with a Node.js runtime that supports built-in `fetch`:

```bash
node encrypted-handoff.mjs
```

This demonstration generates new secrets in memory on every run and verifies
one round trip. To exchange data between processes, distribute the secrets
through an authenticated channel outside Sopholeth.

```javascript
import assert from "node:assert/strict";
import {
  createCipheriv,
  createDecipheriv,
  createHmac,
  randomBytes,
  randomUUID,
} from "node:crypto";

const baseURL = process.env.SOPH_URL ?? "http://localhost:8080";
const encryptionKey = randomBytes(32);
const rendezvousSecret = randomBytes(32);
const context = ["sopholeth-handoff-v1", randomUUID(), "sender", "recipient"];
const storageKey = createHmac("sha256", rendezvousSecret)
  .update(JSON.stringify(context))
  .digest("hex");

// Authenticate the storage key too, so moving the envelope to another key
// does not produce a valid message for that other rendezvous.
const associatedData = Buffer.from(storageKey, "utf8");

function encrypt(plaintext) {
  const nonce = randomBytes(12);
  const cipher = createCipheriv("aes-256-gcm", encryptionKey, nonce);
  cipher.setAAD(associatedData);
  const ciphertext = Buffer.concat([
    cipher.update(plaintext, "utf8"),
    cipher.final(),
  ]);
  // Envelope: nonce (12 bytes), authentication tag (16), ciphertext.
  return Buffer.concat([nonce, cipher.getAuthTag(), ciphertext])
    .toString("base64");
}

function decrypt(envelope) {
  const packed = Buffer.from(envelope, "base64");
  if (packed.length < 28) throw new Error("Truncated encrypted envelope");
  const decipher = createDecipheriv(
    "aes-256-gcm",
    encryptionKey,
    packed.subarray(0, 12),
    { authTagLength: 16 },
  );
  decipher.setAAD(associatedData);
  decipher.setAuthTag(packed.subarray(12, 28));
  // final() throws if authentication fails; never use partial plaintext.
  return Buffer.concat([
    decipher.update(packed.subarray(28)),
    decipher.final(),
  ]).toString("utf8");
}

const url = new URL(`/v1/data/${encodeURIComponent(storageKey)}`, baseURL);
const original = "temporary handoff payload";
const stored = await fetch(url, {
  method: "PUT",
  headers: { "X-TTL": "300" },
  body: encrypt(original),
});
await stored.text();
if (stored.status !== 201 && stored.status !== 202) {
  throw new Error(`Store failed: HTTP ${stored.status}`);
}

const retrieved = await fetch(url);
if (!retrieved.ok) throw new Error(`Retrieve failed: HTTP ${retrieved.status}`);
assert.equal(decrypt(await retrieved.text()), original);
console.log(`Encrypted round trip verified (PUT ${stored.status}).`);
```

`SOPH_URL` is a setting for this example client only. The node's current
configuration still uses the [legacy variables](configuration.md).

## What the envelope does and does not protect

Only clients holding the encryption key can decrypt and generate valid
envelopes. Everyone sharing that key has the same capability; this does not
establish an individual sender's identity.

Nodes still see the storage key, ciphertext length, timing, and connection
metadata. Opaque names do not make traffic anonymous. Listing makes the key
discoverable, and anyone can overwrite or withhold its value.

Authenticated encryption detects modification, but not replay of an earlier
valid envelope at the same rendezvous. Put application sequence numbers,
observation times, or freshness challenges inside the encrypted payload when
the application needs them, and enforce those rules in clients.

TTL limits a cooperating node's local retention. It does not invalidate a
ciphertext copy or encryption key retained elsewhere.

## MCP use

Encrypt in the client before calling the current `repram_store` tool with a
base64 string, and decrypt after `repram_retrieve`. Keep the encryption key
out of tool arguments and payloads. The MCP process receives ciphertext, but
the client environment that performs encryption still has the plaintext and
key.
