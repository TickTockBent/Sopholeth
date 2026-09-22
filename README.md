# Sopholeth

**Wisdom through intentional forgetting.**

Pronounced **SOF-oh-leth**. The client CLI is **`soph`**; see the
[CLI guide](docs/cli.md).

Sopholeth is a distributed network for temporary shared state. Store bytes
under a key with a time-to-live (TTL), let peers carry them, and retrieve them
while they remain live. Each node forgets its copy when its local TTL expires.

Use it for agent handoffs, temporary working state, presence signals, and
applications whose data should expire. Nodes store opaque values without
interpreting their contents. There are no client accounts, key ownership, or
durable delivery guarantees. Encrypt sensitive values before storing them;
expiration cannot make readers forget copies they kept.

## Project status

The Go implementation includes an HTTP node, an embedded MCP server, gossip
replication, WebSocket attachments, signed public discovery, and a topology
dashboard. The next milestones are the **first public network**, a **demo web
application**, and a **probe simulation**.

Public launch is pending: the discovery trust anchor is still a placeholder.
Use a private network for development.

The service executables are `server`, `omega`, and `dashboard`. Internal names
follow their roles: `NODE_*` configuration, plain MCP tool names, and component
metrics. See the [migration guide](docs/rebrand.md) when updating an older
checkout or deployment. The `soph` client talks to any node over HTTP.

## Run locally

From this checkout, with Go 1.22 or later:

```bash
go build -o bin/server ./cmd/server
NODE_NETWORK=private NODE_MAX_STORAGE_MB=50 ./bin/server
```

The HTTP listener uses port 8080 on all interfaces.
`NODE_NETWORK=private` disables public discovery; it does not restrict who
can reach the listener.

In another terminal:

```bash
curl -i -X PUT -H "X-TTL: 300" --data-binary "hello" \
  http://localhost:8080/v1/data/hello
curl http://localhost:8080/v1/data/hello
curl -I http://localhost:8080/v1/data/hello
```

A PUT returns `201` when the node observes its quorum, or `202` when the
write is stored locally but quorum is unconfirmed. Both accept the local
write. Reads and listings are local to the node you contact.

Or use the client. Join once, then every command uses that node:

```bash
go build -o bin/soph ./cmd/soph
./bin/soph join localhost
printf 'hello' | ./bin/soph put hello --ttl 300
./bin/soph get hello
./bin/soph exists hello
./bin/soph list
./bin/soph serve                      # open the printed local viewer URL
```

The [CLI guide](docs/cli.md) covers named networks, output formats, and
exit codes.

For a container with its host port restricted to loopback:

```bash
docker build -t sopholeth/node:local .
docker run --rm -p 127.0.0.1:8080:8080 \
  -e NODE_NETWORK=private -e NODE_MAX_STORAGE_MB=50 \
  sopholeth/node:local
```

This builds a local image from the checkout; it does not depend on a renamed
registry package.

## Connect an agent

Use the absolute path to the locally built binary in your MCP client:

```json
{
  "mcpServers": {
    "sopholeth": {
      "command": "/absolute/path/to/checkout/bin/server",
      "args": ["--mcp"],
      "env": {
        "NODE_NETWORK": "private",
        "NODE_MAX_STORAGE_MB": "50"
      }
    }
  }
}
```

MCP mode embeds the same node, with an OS-assigned HTTP port and logs on
stderr. It also opens an HTTP listener on all interfaces; stdio is the agent
interface, not a network isolation boundary.

The tools are `store`, `retrieve`, `exists`,
and `list_keys`. Set `NODE_PEERS` to a comma-separated list of
reachable `host:httpPort` seeds to join a private cluster. A substrate with
`NODE_INBOUND=true` accepts outbound WebSocket attachments from agents.

## Try a cluster

```bash
docker compose up --build
```

The checked-in configuration starts three private nodes:

| Endpoint | Enclave |
| --- | --- |
| `localhost:8091` | `enclave-a` |
| `localhost:8092` | `enclave-a` |
| `localhost:8093` | `enclave-b` |

Nodes in the same enclave replicate values; different enclaves share topology
without replicating values across the boundary. This setup exercises enclave
separation. It publishes its host ports on all interfaces.

## Documentation

The website projects live in [sites/](sites/README.md): `sopholeth.com` for
marketing and showcases, `sopholeth.io` for docs, `sopholeth.dev` for the devlog
and release notes, and the live node viewer at `soph.stream`. Each directory is an
independent Vercel project root.

- [API and MCP reference](docs/api.md)
- [Node configuration](docs/configuration.md)
- [Usage patterns](docs/patterns.md) and [client-side encryption](docs/encryption-example.md)
- [Architecture](docs/architecture.md) and [core principles](docs/core-principles.md)
- [Signed discovery](docs/discovery.md) and [omega operations](docs/omega-operations.md)
- [First public-network plan: omega, then three roots](docs/public-network-plan.md)
- [Roadmap](docs/roadmap.md) and [rebrand migration](docs/rebrand.md)
- [soph.stream implementation and launch plan](docs/soph-stream-plan.md)
- [Contributing](CONTRIBUTING.md), [validation harness](test/burnin/README.md), and [changelog](CHANGELOG.md)

See [LICENSE](LICENSE) for the current licensing terms.
