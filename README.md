# toolbox

A Go module of reusable building blocks for **agent memory and coordination**, extracted from [signatory](https://github.com/sarahmaeve/signatory). The packages assemble into a typed, schema-validated message bus that an MCP-capable LLM client (Claude Code today; anything else tomorrow) can talk to — without burning per-token API budget on coordination.

## Layout

```
pkg/
  schema/           narrow JSON Schema 2020-12 validator; structured *Violation
  mcp/              JSON-RPC 2.0 / MCP 2025-11-25 server framework
  messagestore/     SQLite-backed sessions + messages with a MessageType registry
  bridge/           localhost HTTPS server + Go client over the messagestore
  certs/            mkcert CA bootstrap + shell profile patching
cmd/
  toolbox-bridge/   HTTPS bridge + init/doctor/lifecycle commands
  toolbox-mcp/      MCP-over-stdio server exposing the messagestore as tools
```

Each package is independently consumable. Dependencies flow downward:

```
                          bridge          (HTTPS layer)
                           │  ╲
                  ┌────────┘   ╲
                  ▼             ▼
            messagestore     certs        (persistence + TLS trust)
                  │
                  ▼
               schema                     (foundational validator)
                                          mcp (independent; uses schema)
```

## What you get

- **Two-stage validation** on every message: structural via `pkg/schema` at ingest, semantic via an optional `OnIngest` Go hook per `MessageType`. Modeled on signatory's MCP-tool + analyst-output split, which catches LLM payload errors in a form the LLM can self-correct from in one turn.
- **Versioned SQLite migrations** with automatic timestamped backups before each step. TOCTOU-safe DB file creation, 0600 perms, WAL, FK enforcement, single-connection pool.
- **Localhost HTTPS bridge** that exists because some agent HTTP clients (notably Claude Code's WebFetch) are GET-only or refuse self-signed certs. mkcert + a managed CA anchor at a project-owned path solves both.
- **MCP server framework** with strict-reject schema validation (`additionalProperties:false`), oversize-frame recovery, race-safe lifecycle handshake, uniform `Response{Status, Data, Error, Metadata}` envelope.
- **Pull-based coordination**: no event bus, no fan-out delivery semantics. Producers `DepositMessage`; consumers `GetLatestMessage` / `GetMessages` with optional `role` / `sender_id` / `type` / `subject_id` filters.
- **Day-one ergonomics**: `init` bootstraps a fresh machine; `doctor` reports breadth-first on local health; `serve start/stop/restart/status` runs the bridge as a managed daemon.

## Default paths

All under **`~/.toolbox/`**, deliberately distinct from signatory's `~/.signatory/` so both can coexist:

| What | Path |
|---|---|
| Database | `~/.toolbox/messages.db` |
| CA anchor | `~/.toolbox/certs/rootCA.pem` |
| Server cert / key | `~/.toolbox/certs/127.0.0.1+1.pem` + `-key.pem` |
| PID file | `~/.toolbox/run/bridge.pid` |
| Log file | `~/.toolbox/log/bridge.log` |
| Schemas | `~/.toolbox/schemas/` |

Every path is overridable by flag — see `toolbox-bridge serve run --help`.

## Quick start

Prerequisites:

- Go 1.26+
- [mkcert](https://github.com/FiloSottile/mkcert), already initialized via `mkcert -install`
- macOS or Linux

```bash
git clone git@github.com:sarahmaeve/toolbox.git
cd toolbox
make install                                       # builds + installs to $GOBIN with version stamp
toolbox-bridge init --write-profile --seed-schemas # one-time bootstrap
source ~/.zshrc                                    # picks up NODE_EXTRA_CA_CERTS
toolbox-bridge serve start                         # daemonize the HTTPS bridge
toolbox-bridge doctor                              # confirm everything green
```

`init` errors out with install hints if `mkcert` is missing or `mkcert -install` hasn't been run — it never installs anything itself.

## Subcommand surface

```
toolbox-bridge init [--write-profile] [--seed-schemas]   one-time bootstrap
toolbox-bridge doctor [--strict]                          read-only diagnostic
toolbox-bridge serve run     [flags]                      foreground
toolbox-bridge serve start   [flags]                      daemonize
toolbox-bridge serve stop    [--pid-file path]
toolbox-bridge serve restart [flags]
toolbox-bridge serve status  [--pid-file path]
toolbox-bridge certs init    [--write-profile]            lower-level (init covers this)
toolbox-bridge certs check                                lower-level (doctor covers this)
toolbox-bridge version
```

`toolbox-mcp` is stdio-only — its lifecycle is owned by the MCP client (Claude Code spawns and reaps it). See `.mcp.json.example` for wiring.

## MCP integration with Claude Code

```bash
cp .mcp.json.example /your/project/.mcp.json
# Edit /your/project/.mcp.json: replace YOUR_USERNAME with your actual user
# (Claude Code does not expand ~ or $HOME — paths must be absolute)
claude                                             # run in your project
```

The toolbox MCP server registers six tools: `create_session`, `deposit_message`, `list_sessions`, `get_session`, `get_messages`, `get_latest_message`. All payload schemas are loaded from `--schemas-dir` at startup; the registry is in-memory per process. The bridge daemon and the MCP server share the same SQLite database by default, so messages deposited via one are immediately visible via the other.

## Design decisions worth knowing

1. **Schemas must be strict-reject** (`additionalProperties:false`). Permissive schemas are refused at registration so an LLM never gets to add silent "extra" fields that drift the contract.
2. **Role and Type are orthogonal**. Role = who emitted (agent, orchestrator, user, …); Type = what payload shape (schema-validated). `Config.AllowedRoles` controls the role vocabulary; `RegisterType` controls the type vocabulary.
3. **SenderID is finer than Role; SubjectID groups across sessions.** Role is a coarse vocabulary (4–6 values); SenderID is the precise producer (`agent.code-reviewer.v2`). SubjectID is an external reference (ticket, file path) so messages about the same subject across many sessions can be retrieved together.
4. **The MessageType registry is in-memory at startup**, never persisted. A process owns its types; multi-process setups must register the same types in each.
5. **`InsecureSkipVerify` is never set**, even on localhost. Scheme dispatch (`http://` for tests, `https://` for production) chooses the TLS path without an opt-out flag.
6. **Pull, not push**. No handler fan-out. Consumers poll. This trades push-side ergonomics for fewer delivery-semantics surprises (no retries, no dead-letter, no ordering guarantees to break).
7. **We never run installers ourselves.** `init` and `doctor` error out with install hints when `mkcert` or `mkcert -install` is missing — modifying the system trust store and installing system packages stays the user's call.

## Development

```
make check        gofmt + go vet + go test -race ./...
make test         unit tests, no race
make test-race    unit tests with -race
make build        ./bin/{toolbox-bridge,toolbox-mcp} with version stamping
make doctor       run toolbox-bridge doctor against this machine
make clean        rm ./bin
```

All packages run race-clean. Integration tests in `pkg/bridge` exercise the full HTTP round-trip against a real `messagestore.Store` via `httptest`.

## License

MIT, same as the parent project.
