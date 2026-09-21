# Sopholeth signed public discovery

Signed discovery is implemented in the Go node. Public activation still
requires a real omega trust anchor, deployed roots, and DNS publication.
The current compiled key is a placeholder. Use private mode for ordinary
development until the launch work is complete.

This document replaces the former 2.1 proposal with the current contract and
its known implementation limits.

## Trust boundary

The binary contains an Ed25519 public key and the format identifier
`omega-v1`. The operator holds the corresponding private key offline and
uses it to sign public root lists. DNS distributes those lists; the compiled
key authenticates them.

This establishes which addresses may bootstrap the public network. It does
not authenticate all subsequent topology information, provide per-node
identities, or protect client values. Optional peer HMAC is a separate
mechanism configured by an operator; a node does not learn a shared secret
from a bootstrap response.

## DNS records

The current binary queries `_bootstrap.sopholeth.io`. Its TXT value points to
the signed-list record:

```text
omega=_omega.sopholeth.io
```

These names are compiled into the current release source. Public operation
still requires domain ownership, DNS publication, deployed roots, and a real
trust anchor; the rebrand does not publish records or activate a network.

The target TXT value has four semicolon-separated fields:

```text
v=omega-v1;exp=<unix-seconds>;nodes=root-a.example:8080,root-b.example:8080;sig=<base64-signature>
```

This is a format illustration, not a valid record. Use the
[signing tool](omega-operations.md) to generate a record with a fresh
expiration and a real signature.

| Field | Meaning |
| --- | --- |
| `v` | Must match the binary's compiled format version. |
| `exp` | Unix expiration timestamp in seconds; invalid at or after this time. |
| `nodes` | Comma-separated root addresses using their **HTTP ports**. |
| `sig` | Base64-encoded Ed25519 signature. |

Publish one nonempty TXT record per lookup name. The resolver selects the
first nonempty record; multiple candidate records are not a version-negotiation
mechanism. Split strings within one TXT record are concatenated by the DNS
resolver before parsing.

## Canonical signature payload

The signature covers the UTF-8 bytes of:

```text
v=omega-v1;exp=<unix-seconds>;nodes=<lexicographically-sorted-addresses>
```

Fields appear in the order `v`, `exp`, `nodes`, separated by semicolons,
without inserted whitespace. Addresses are sorted and joined with commas.
The `sig` field is excluded. The signer appends
`;sig=<base64-signature>` for transport.

The parser rejects missing, duplicate, unknown, and malformed fields.
Verification additionally checks version, expiration, signature length, and
signature validity. Unknown fields require a new signed format version;
they cannot be treated as authenticated extensions to `omega-v1`.

The implementation is in [signedlist.go](../internal/trust/signedlist.go),
with [format tests](../internal/trust/signedlist_test.go).

## Startup and cache

With public mode and no manual peers, the node:

1. Resolves the bootstrap pointer and signed list.
2. Parses and verifies the list against the compiled key and current time.
3. Caches the verified list and uses its addresses as seeds.
4. Marks itself as a root if its exact advertised `address:httpPort` appears
   in the list.

If DNS lookup or verification fails, startup attempts the on-disk cache and
verifies it again. Without a valid unexpired list from either source,
startup fails. There is no unsigned DNS fallback.

The cache is `root-list.json` in the configured
[cache directory](configuration.md#operations). It contains public signed
metadata. Failure to persist a freshly verified list is logged and does not
prevent using it in memory.

In public mode, non-roots return `403` from `POST /v1/bootstrap`. Private
mode uses manual peers and does not apply the public-root gate. Setting
manual peers in public mode skips verification and leaves root status false.

## Refresh and current limits

A background refresher requests a new list before expiration with jitter.
Successful verification updates the cache, current seeds, and root membership.
Failed refreshes retain the previous list and retry with backoff. Isolation
recovery uses the refresher's current seeds.

**Running-node expiration remains unresolved.** Startup rejects an expired
list, but the current refresher retains its last list on failure and root
status changes only after a successful update. A running root is not
automatically demoted when that retained list expires, and recovery can
continue using its old seeds. Correcting and testing this is a
[public-alpha gate](roadmap.md#before-public-alpha).

Removing a root from a newly signed list takes effect at each node when it
accepts that update. Older unexpired signed lists can still be replayed until
they expire; there is no independent revocation channel.

Key and format rotation need a coordinated binary and discovery rollout.
The current client supports one compiled key/version and one bootstrap name.
Publishing multiple versions or changing a shared pointer does not by itself
let old and new clients select different trust anchors.
