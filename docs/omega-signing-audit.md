**Sopholeth omega audit — 2026-09-22**

Audited revision: `4b89751b069b02ff81b006b6268ed321b883af55` on `main`, after merging [PR #191](https://github.com/TickTockBent/Sopholeth/pull/191). All PR checks passed. The audit made no changes to omega implementation, production keys, configuration, or DNS records. This report records the interim implementation before the public-network redesign.

The review covered key generation and storage, signing and parsing, the compiled trust anchor, DNS resolution, caches, running-node refresh and bootstrap authorization, CLI discovery, dashboard consumers, and the publication runbook/lab scripts. Findings combine source inspection with nine local reproduction tests against the actual packages using Go's file-overlay mechanism. Test keys and records were disposable; no public DNS queries were redirected or published.

**Assessment**

There is a better operational design available. Today one offline signing key authorizes every root-list change and every routine extension of that list's lifetime. An unchanged network still needs recurring offline signing and DNS updates. The protocol also has no authenticated key-rotation path or record sequence. Moving the same key into a scheduled job would reduce manual work while concentrating authority in that job.

Before public activation, address the confirmed implementation defects below and choose the key lifecycle. A new production key alone would fix the placeholder problem but leave the expiration, transport, rollback, rotation, and operational issues.

**Issue index**

| Finding | Scope | Tracking issue |
| --- | --- | --- |
| 1 | Reject the forgeable placeholder anchor | [#192](https://github.com/TickTockBent/Sopholeth/issues/192) |
| 2 | Enforce runtime authority expiration | [#193](https://github.com/TickTockBent/Sopholeth/issues/193) |
| 3 | Authenticate bootstrap connections | [#194](https://github.com/TickTockBent/Sopholeth/issues/194) |
| 4 | Define graceful key rotation through soph omega | [#195](https://github.com/TickTockBent/Sopholeth/issues/195) |
| 5 | Prevent metadata rollback | [#160](https://github.com/TickTockBent/Sopholeth/issues/160) |
| 6 | Make keypair creation exclusive and recoverable | [#196](https://github.com/TickTockBent/Sopholeth/issues/196) |
| 7 | Validate signing inputs and emitted metadata | [#197](https://github.com/TickTockBent/Sopholeth/issues/197) |
| 8 | Correct refresh retry scheduling | [#172](https://github.com/TickTockBent/Sopholeth/issues/172) |
| 9 | Refresh public CLI discovery automatically | [#198](https://github.com/TickTockBent/Sopholeth/issues/198) |
| 10 | Automate renewal and verify publication | [#199](https://github.com/TickTockBent/Sopholeth/issues/199) |

Findings 5 and 8 reuse existing tickets. Runtime expiration was split out of #172 into #193; selected trust items from roll-up #178 now have focused tickets. Public cutover remains tracked separately in [#80](https://github.com/TickTockBent/Sopholeth/issues/80). The subsequent issue triage replaced its historical cutover instructions; the [public-network plan](public-network-plan.md) records the agreed omega-first, three-root delivery path.

**Findings, ordered by consequence**

1. **Critical for current placeholder builds: forged discovery records pass verification without a private key.**

   [The compiled anchor](https://github.com/TickTockBent/Sopholeth/blob/4b89751b069b02ff81b006b6268ed321b883af55/internal/trust/omega.go#L29) is 32 zero bytes. The decoder checks encoding and length only. With the repository's Go 1.22.2 toolchain, this point permits verification of constructed signatures for selected messages. The local reproduction passed a forged record through the full `FetchSigned` path and obtained the fixture's unauthorized address. The same record failed under a normally generated key.

   Exploitation requires delivering that forged record through discovery or its cache; the audit used an in-memory resolver. This finding concerns the placeholder, not properly generated Ed25519 keys. Public discovery should explicitly reject an unset/placeholder anchor, and public release validation should assert the expected real anchor. Keep development/test anchors explicit and separate.

2. **High: expired discovery authority remains active in running nodes.**

   [Current()](https://github.com/TickTockBent/Sopholeth/blob/4b89751b069b02ff81b006b6268ed321b883af55/internal/trust/refresher.go#L124) returns the retained list without checking expiration. Failed refreshes leave it in place and do not notify the root-status callback. The server's [recovery seed provider](https://github.com/TickTockBent/Sopholeth/blob/4b89751b069b02ff81b006b6268ed321b883af55/cmd/server/main.go#L233) consumes that list, and its [bootstrap gate](https://github.com/TickTockBent/Sopholeth/blob/4b89751b069b02ff81b006b6268ed321b883af55/cmd/server/main.go#L911) consults a boolean set after successful verification. Dashboard roots also remain derived from the retained list.

   Reproduction advanced a controlled clock beyond expiration, failed a refresh, and confirmed the expired list remained available with no authorization update. Startup verification rejects expiration, so restart and continued operation have inconsistent trust behavior. Expiration needs its own enforced transition, independent of DNS success. Existing peer traffic and permission to act as a trusted bootstrap root should have explicitly separate lifecycle rules.

3. **High deployment gap: the signed address is followed by an unauthenticated root connection.**

   [Node bootstrap](https://github.com/TickTockBent/Sopholeth/blob/4b89751b069b02ff81b006b6268ed321b883af55/internal/gossip/bootstrap.go#L103) constructs `http://<signed-address>/v1/bootstrap`. The CLI likewise defaults bare signed addresses to HTTP. Bootstrap responses have no signature tied to the omega authorization. Subsequent gossip and WebSocket connections also use HTTP/WS. A network attacker capable of redirecting hostname resolution or modifying the connection can supply bootstrap topology despite a correctly signed TXT record.

   This was established by tracing the transport and response-validation paths; no external interception was attempted. Signed discovery currently authenticates the address list, as the docs acknowledge. For public deployment, authenticate the actual bootstrap connection, preferably with explicit HTTPS endpoints and certificate validation. Signing discovery metadata does not solve that connection problem by itself. HTTPS endpoint handling must be consistent across consumers; adding `https://` to today's list breaks node bootstrap URL construction.

4. **High recovery/design gap: routine key rotation requires a coordinated binary migration.**

   [The trust format](https://github.com/TickTockBent/Sopholeth/blob/4b89751b069b02ff81b006b6268ed321b883af55/internal/trust/signedlist.go#L43) has one format version, one signature, an expiration, and addresses. There is one compiled key. No signed delegation, key identifier set, threshold, or authenticated successor-key chain exists. Replacing the compiled key creates clients with incompatible anchors; publishing multiple TXT records does not negotiate between them.

   This is a design limitation, not a reproduced signature bypass. Define normal rotation, compromised-key recovery, stale-client recovery, and signer unavailability before establishing the production anchor. Increasing record lifetimes reduces signing frequency while increasing the lifetime of old authorizations.

5. **Medium: a previously valid list can roll back a newer accepted list.**

   [refreshOnce](https://github.com/TickTockBent/Sopholeth/blob/4b89751b069b02ff81b006b6268ed321b883af55/internal/trust/refresher.go#L169) accepts any correctly signed unexpired list, overwriting current memory and cache. A reproduction initialized a newer list, delivered an older still-valid list that named a retired root, and confirmed the older list became current and was persisted.

   The current documentation acknowledges replay of unexpired records. A sequence/version with persisted monotonic acceptance would prevent rollback after a client has observed newer metadata. First-use clients still need freshness/expiration policy; sequence numbers alone cannot establish that an unseen newer list exists.

6. **Medium: key creation's no-overwrite guarantee fails under concurrency.**

   [atomicWriteNoClobber](https://github.com/TickTockBent/Sopholeth/blob/4b89751b069b02ff81b006b6268ed321b883af55/cmd/omega/main.go#L175) checks `Stat` and later uses ordinary `Rename`, which can replace a file created between those steps. A local concurrent test recorded two successful writers to the same destination. Real concurrent key generation can therefore replace key material or leave an inconsistent pair.

   Separately, a pre-existing public-output file makes `keygen` fail only after it has created a new private key. That partial result was reproduced. Use exclusive publication, validate both destinations before writing, and make recovery from partial creation explicit. File/directory synchronization is also absent, so atomic visibility should not be described as power-loss durability.

7. **Medium: signing can succeed while producing records consumers reject.**

   [runSign](https://github.com/TickTockBent/Sopholeth/blob/4b89751b069b02ff81b006b6268ed321b883af55/cmd/omega/main.go#L101) returned success for a comma-only node list, unsupported format version, a lifetime that expired within the current second, and a node string containing record delimiters. Each resulting record failed parsing or verification. Separately, validly signed records containing missing/invalid ports, schemes, paths, and fragments passed the trust layer despite consumers expecting `host:port`.

   [Private-key decoding](https://github.com/TickTockBent/Sopholeth/blob/4b89751b069b02ff81b006b6268ed321b883af55/cmd/omega/main.go#L146) checks only base64 and byte length. A 64-byte key with a corrupted public half was accepted and generated an invalid signature. Validate the key against its derived public half and expected authority, validate the manifest, and round-trip/verify the exact emitted record before reporting success. Add operator commands for inspection, verification, and publication checks.

8. **Medium: refresh failure backoff is followed by another normal refresh delay.**

   In [Run](https://github.com/TickTockBent/Sopholeth/blob/4b89751b069b02ff81b006b6268ed321b883af55/internal/trust/refresher.go#L135), failure waits for the backoff and then restarts the outer loop, which calculates another normal rotation delay. A controlled-clock test triggered a refresh early in a one-hour lease; after its 30-second backoff, the loop scheduled a further roughly 55-minute wait instead of retrying. This reduces the useful recovery window.

   Separate scheduled refresh from retry scheduling. Add an explicit per-attempt deadline, bound attempts by lease expiration, and record expiration/failure metrics rather than only the last successful refresh time.

9. **Medium operational gap: public CLI profiles require manual rejoining as leases expire.**

   [selectNetwork](https://github.com/TickTockBent/Sopholeth/blob/4b89751b069b02ff81b006b6268ed321b883af55/cmd/soph/commands.go#L50) rejects a saved public profile after its captured `RootsExpire`, even when newer valid discovery exists. It directs the user to run `soph join` again. Existing `soph serve` processes check selection at startup and have no discovery refresh lifecycle.

   Public clients need bounded rediscovery integrated with profile selection and long-lived viewing. A metadata refresh must preserve known write outcomes and must not implicitly retry PUTs. Shortening discovery lifetimes otherwise creates recurring user maintenance as well as operator maintenance.

10. **Medium operational gap: publication lacks a production verification/renewal loop.**

    The [operator runbook](https://github.com/TickTockBent/Sopholeth/blob/4b89751b069b02ff81b006b6268ed321b883af55/docs/omega-operations.md) requires manual transfer and publication. There is no production publisher that verifies the intended record after propagation or alerts on remaining authorization lifetime. The existing [lab loop](https://github.com/TickTockBent/Sopholeth/blob/4b89751b069b02ff81b006b6268ed321b883af55/test/burnin/sign-loop.sh#L72) logs a warning after dnsmasq restart failure and then prints `OK published`; it does not verify the served result. This script is documented as a lab helper, so it should not become the production workflow by accident.

    The resolver also selects the first nonempty TXT record; DNS record ordering is unsuitable for version negotiation. Reject ambiguous publication explicitly or use a format with a deterministic authenticated update procedure. Publication should expose the active version, expiration, root set, expected key fingerprint, and verification result.

**What is working**

Generated keys use Go's Ed25519 implementation and cryptographic randomness. Private-key files are created with mode 0600. Canonical signing fixes field order and sorts addresses. Parsing rejects duplicate and unknown fields; verification rejects wrong versions, expiration, tampering, and wrong real keys. Startup cache contents are reverified; cache writes replace files atomically. Tests cover these paths. These useful foundations can be retained where they fit the selected design.

**Recommended direction**

Separate infrequent authority/membership changes from frequent freshness renewal. I recommend evaluating TUF for distributing a small bootstrap manifest over HTTPS. TUF defines distinct root, targets, snapshot, and timestamp roles, version checks, and authenticated key rotation. Its online timestamp role provides routine freshness while higher-authority keys can remain offline. These are relevant established mechanisms; applying them to Sopholeth discovery is my recommendation. [TUF specification](https://theupdateframework.github.io/specification/latest/)

For this project, the manifest would describe the public network, the default enclave, and typed bootstrap endpoints. Review and sign membership changes; automate short-lived freshness publication for unchanged approved content. Preserve independently enforceable expiration in each consumer. Provide one operator workflow that checks the candidate, signs through the chosen key store, publishes, and verifies what clients actually receive. DNS can locate the repository, while the client retains an authenticated trust anchor. Runtime topology remains outside the discovery-signing authority.

The [Go TUF project](https://github.com/theupdateframework/go-tuf) provides metadata and client-update APIs. Library selection needs a small compatibility spike: its inspected [master go.mod](https://raw.githubusercontent.com/theupdateframework/go-tuf/master/go.mod) declares Go 1.27.0, while this repository uses Go 1.22.2. That is an integration cost to assess against a reviewed release/toolchain plan, not a reason to reimplement its trust protocol casually.

| Approach | Operator benefit | Remaining cost/tradeoff |
| --- | --- | --- |
| Harden the current format and lengthen list lifetimes | Smallest code change; fewer signing sessions | Manual renewal and key migration remain; old authorizations last longer |
| Automate the current authority key in a protected signing service | Removes routine manual signing | The same credential can authorize arbitrary roots; rotation still requires the current binary migration |
| Separate authority and freshness using TUF | Routine renewal can run unattended; key lifecycle has defined mechanisms | Additional metadata, tooling, dependency/toolchain integration, and migration tests |

My preference is the third approach before public launch, with a small prototype to establish the actual integration cost. For the initial small root set, membership changes can remain an explicit operator action while freshness renewal is automated. Changing the transport or key-storage provider alone would not fix current lease enforcement, rollback acceptance, or record validation.

**Operator experience and graceful rotation**

The initial implementation was an interim testing workflow. The agreed interface is the existing `soph` CLI, with operator commands under `soph omega`; fold in and retire the standalone `omega` binary as part of the redesign. The subcommand behavior below is proposed, not implemented. TUF remains a candidate to evaluate, not an adopted dependency or protocol.

The production replacement should make these tasks explicit and repeatable:

- `soph omega init`: establish the authority, recovery material, and initial signed trust metadata; produce a public bootstrap bundle for the release without copying private authority keys onto nodes.
- `soph omega publish`: validate a reviewed bootstrap-node manifest, prepare its signed metadata, publish it, and verify the externally served result. Repeating publication of the same approved content should be safe.
- `soph omega status`: show the accepted trust/manifest versions, key fingerprints, expiration deadlines, publication verification, and required operator action.
- `soph omega rotate`: guide a planned successor-key transition and emit a reviewable rotation plan. Routine freshness renewal continues through a separate automated publisher.

A documented normal rotation should cover: create the successor key; authorize its role using the current authority; publish the required signed transition and successor metadata; retain the metadata needed by older clients; verify adoption and recovery in representative clients; retire the former operational key according to an explicit overlap policy. Authority-key rotation needs the selected protocol's old-and-new authorization checks. Normal rotations should not require rebuilding every client. Key loss and compromise require their own recovery procedures; complete loss/compromise of the ultimate trust authority cannot be repaired merely by publishing an unsigned replacement.

Document operator custody, which steps require the protected authority, publisher credentials, renewal ownership, version retention, overlap/retirement criteria, and failure recovery. Exact lifetimes and overlap periods should be chosen from the intended outage tolerance and client-offline behavior. Keep the operational interface small even if its underlying trust protocol has several metadata roles.

**Implementation order after triage**

The issues describe required outcomes for the public implementation. Replacing an interim component can satisfy a finding; retaining the current signing format is not a requirement.

1. Reject the placeholder, choose the authority/freshness model and authenticated bootstrap transport, and prototype client integration and operator ergonomics using disposable keys.
2. Deliver the `soph omega` suite with expiration, rollback, retry, initialization, validation, and verified publication behavior in that selected design. Carry the audit reproduction cases into regression tests; rehearse renewal, rotation, outage, and stale-client recovery.
3. Resolve root-facing launch defects and document/rehearse the three-root deployment, using the HTTP CLI and viewer. MCP and broader transient features are deferred as described in the public-network plan.
4. Establish production custody and recovery, prepare a verifiable release, then publish and activate the three default-enclave public roots through the rehearsed procedure.

**Validation and limits**

The existing race-test suites passed for `internal/trust`, `cmd/omega`, `internal/cluster`, `cmd/server`, `internal/gossip`, `cmd/soph`, and `internal/dashboard`. Nine additional audit tests confirmed the behaviors described above; their PASS results mean the defect was reproduced, not that the implementation passed a security requirement. The audit-only overlays were kept outside the repository. The nine checks exercised placeholder forgery through discovery; retention of an expired list after refresh failure; rollback of memory and cache; structurally invalid signed endpoints; successful signing of unusable records; inconsistent private-key halves; concurrent writes to a key destination; partial keypair creation; and the extra delay after refresh backoff. These are observed failures, not permanent regression tests. The linked implementation issues specify the regression coverage to add.

The review did not validate production DNS control, host access, key custody, or a deployed public network. Transport and key-rotation findings are source/design findings; the listed key-generation, signing, placeholder-verification, expiration-retention, rollback, and retry findings have local executable reproductions. No implementation changes were started.
