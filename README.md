# toolbox

A Go module of reusable building blocks for **agent memory and coordination**, extracted from [signatory](https://github.com/sarahmaeve/signatory). The packages assemble into a typed, schema-validated message bus that an MCP-capable LLM client (Claude Code today; anything else tomorrow) can talk to — without burning per-token API budget on coordination.

## Layout

```
pkg/
  schema/           narrow JSON Schema 2020-12 validator; structured *Violation
  mcp/              JSON-RPC 2.0 / MCP 2025-11-25 server framework
  messagestore/     SQLite-backed sessions + messages with a MessageType registry
  messagetypes/     canonical MessageTypes baked into the binaries (e.g. task)
  bridge/           localhost HTTPS server + Go client over the messagestore
  certs/            mkcert CA bootstrap + shell profile patching
  pdf/              stdlib-only PDF 1.4–1.7 text + image extractor
  pdfclean/         post-process extracted text into a markdown working copy
cmd/
  toolbox-bridge/   HTTPS bridge + init/doctor/lifecycle commands
  toolbox-mcp/      MCP-over-stdio server exposing the messagestore + PDF as tools
  toolbox-pdf/      consolidated CLI for PDF dump / images / clean
```

Each package is independently consumable. Dependencies flow downward:

```
   bridge       (HTTPS layer)               toolbox-pdf
    │  ╲                                        │
    │   ╲                                       ▼
    ▼    ▼                              pdf  ←  pdfclean
 messagestore  certs                    (stdlib-only PDF stack)
    │
    ▼
  schema                                 mcp (independent; uses schema)
```

## What you get

- **Two-stage validation** on every message: structural via `pkg/schema` at ingest, semantic via an optional `OnIngest` Go hook per `MessageType`. Modeled on signatory's MCP-tool + analyst-output split, which catches LLM payload errors in a form the LLM can self-correct from in one turn.
- **Versioned SQLite migrations** with automatic timestamped backups before each step. TOCTOU-safe DB file creation, 0600 perms, WAL, FK enforcement, single-connection pool.
- **Localhost HTTPS bridge** that exists because some agent HTTP clients (notably Claude Code's WebFetch) are GET-only or refuse self-signed certs. mkcert + a managed CA anchor at a project-owned path solves both.
- **MCP server framework** with strict-reject schema validation (`additionalProperties:false`), oversize-frame recovery, race-safe lifecycle handshake, uniform `Response{Status, Data, Error, Metadata}` envelope.
- **Pull-based coordination**: no event bus, no fan-out delivery semantics. Producers `DepositMessage`; consumers `GetLatestMessage` / `GetMessages` with optional `role` / `sender_id` / `type` / `subject_id` filters.
- **Day-one ergonomics**: `init` bootstraps a fresh machine; `doctor` reports breadth-first on local health; `serve start/stop/restart/status` runs the bridge as a managed daemon.
- **PDF processing**: stdlib-only PDF 1.4–1.7 parser that handles compressed cross-reference streams and object streams (common in government/military publications). Extracts text per-page and images as XObjects with bounding boxes, with optional adjacency-based panel stitching for multi-panel figures. The `pdfclean` companion turns raw extracted text into a markdown working copy.

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
```

`init` errors out with install hints if `mkcert` is missing or `mkcert -install` hasn't been run — it never installs anything itself.

Next, register the MCP server at **user scope** so its tools are visible in every Claude Code session, regardless of CWD:

```bash
claude mcp add toolbox -- "$HOME/go/bin/toolbox-mcp" \
  --db "$HOME/.toolbox/messages.db" \
  --schemas-dir "$HOME/.toolbox/schemas" \
  --allowed-roles "user,agent,orchestrator" \
  --log "$HOME/.toolbox/log/mcp.log"
```

(Check `claude mcp --help` if the exact syntax differs in your CLI version.) Then start the bridge daemon and confirm everything is wired up:

```bash
toolbox-bridge serve start                         # manual: alive until reboot or stop
toolbox-bridge doctor                              # everything green
```

For an always-on bridge that survives reboots, see [Running under launchd](#running-the-bridge-under-launchd-macos) below.

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

`toolbox-mcp` is stdio-only — its lifecycle is owned by the MCP client (Claude Code spawns and reaps it). See [MCP integration with Claude Code](#mcp-integration-with-claude-code) below for the recommended user-scope registration, or `.mcp.json.example` for per-project alternative wiring.

```
toolbox-pdf dump   [-page N | -pages N-M] <file.pdf>
toolbox-pdf images [-out DIR] [-page N | -pages N-M] [-no-stitch] [-stitch-tol PT] <file.pdf>
toolbox-pdf clean  [-manifest path -imgdir relpath] <input.txt> <output.md>
toolbox-pdf version
```

## PDF processing

Three operations on digital PDFs (text-based, PDF 1.4–1.7, including compressed cross-reference streams and object streams). Scanned PDFs using JBIG2 are not supported — fall back to Poppler's `pdfimages` for those.

```bash
toolbox-pdf dump my.pdf > raw.txt                           # extract text
toolbox-pdf images -out ./images my.pdf                     # extract images + manifest.tsv
toolbox-pdf clean -manifest ./images/manifest.tsv \
                  -imgdir images raw.txt clean.md           # markdown working copy
```

The same operations are exposed as MCP tools (`pdf_extract_text`, `pdf_extract_pages`, `pdf_extract_images`, `pdf_clean_text`) so a Claude Code session can drive them inline. `pdf_extract_images` writes files to disk and returns the manifest — never image bytes — so a 200-page document with hundreds of figures doesn't blow up an MCP frame.

## MCP integration with Claude Code

Two scopes available:

**User scope (recommended).** Registered once via `claude mcp add` (see [Quick start](#quick-start) above). Available in every Claude Code session regardless of CWD — ideal for utility tools you reach for from anywhere. The toolbox is built for this scope: every default path is under `~/.toolbox/`, so one registration covers all projects.

**Project scope (alternative).** Place a `.mcp.json` in a project root and Claude Code auto-loads it when launched from there. Use this only when you genuinely want per-project isolation — e.g. a project that should see a *different* `--schemas-dir` or `--allowed-roles` set than the user-scope default.

```bash
cp .mcp.json.example /your/project/.mcp.json
# Edit /your/project/.mcp.json: replace YOUR_USERNAME with your actual user
# (Claude Code does not expand ~ or $HOME — paths must be absolute)
```

The toolbox MCP server registers eleven tools total: seven over the messagestore (`create_session`, `deposit_message`, `list_sessions`, `get_session`, `get_messages`, `get_latest_message`, `list_tasks`) and four over the PDF stack (`pdf_extract_text`, `pdf_extract_pages`, `pdf_extract_images`, `pdf_clean_text`). The bridge daemon and the MCP server share the same SQLite database by default, so messages deposited via one are immediately visible via the other.

`get_messages` and `get_latest_message` accept either `session_id` (in-session lookup) or one of `sender_id` / `subject_id` / `role` / `type` (cross-session search). The cross-session mode is the memory-aid path — `get_messages(subject_id="ticket-1234")` returns every digest ever deposited about that topic, regardless of which run produced it. The bridge enforces "at least one filter set" so an empty query never sweeps the whole log; the error message names the acceptable filter fields so an LLM client can self-correct.

## Built-in MessageTypes

Both binaries register a canonical set of MessageTypes at startup (see `pkg/messagetypes`). Schemas and semantic rules are baked into the binary in Go — a user editing JSON in `--schemas-dir` cannot redefine or shadow them. This is deliberate: anything we want agents to use **the same way every time** lives in code, not in a file an agent might also be able to write to.

### `task`

The first canonical type. One message per state transition, keyed by `subject_id`; the latest message wins as "current state"; the full history is the message stream.

| Field | Type | Required | Notes |
|---|---|---|---|
| `title` | string | yes | human-readable summary |
| `status` | enum | yes | `new`, `in-progress`, `done`, `abandoned` |
| `priority` | integer 0–5 | no | |
| `assignee` | string | no | conventional; not validated |
| `notes` | string | no | free-form annotation on this transition |
| `blocker` | string | no | what's holding the task, if status=in-progress |

`additionalProperties:false`, so unknown fields are rejected with the field name in the error. Status is enum-validated by `pkg/schema`; the rejection lists every acceptable enum value:

```
field "status" in input for task: value "started" not in enum [abandoned, done, in-progress, new]
```

That error is the documentation. An agent that picks the wrong value self-corrects in one turn — no source-code access, no docs lookup.

**Usage patterns** that fall out of the design:

- *Create a task:* `deposit_message(type="task", subject_id="ship-the-thing", content={"title": "...", "status": "new"})`. The subject_id is the task's stable identifier across all its messages.
- *Update status:* deposit another task message with the same subject_id and the new status. Optionally include `notes` explaining the transition.
- *Current state of one task:* `get_latest_message(subject_id=<task>, type="task")`.
- *Full audit trail:* `get_messages(subject_id=<task>, type="task")` returns every transition in order.
- *Queue view:* the `list_tasks` MCP tool returns one entry per task (deduplicated across history) with optional `status` filter — "what's currently new?", "what's in flight?", "what shipped recently?".

The self-documenting-error contract extends to ingest-layer rejections too:

```
ErrUnknownRole: "imposter"; allowed roles: [agent, orchestrator, user]
ErrUnknownType: "task.completed"; registered types: [task]
```

An agent encountering either error sees the legitimate vocabulary inline and corrects on the next call.

## Running the bridge under launchd (macOS)

For an always-on bridge that survives reboots, install the example launchd user agent:

```bash
# Copy the example into ~/Library/LaunchAgents and edit YOUR_USERNAME paths
cp examples/launchd-bridge.plist \
   ~/Library/LaunchAgents/com.sarahmaeve.toolbox-bridge.plist
# Replace YOUR_USERNAME everywhere with `whoami`
$EDITOR ~/Library/LaunchAgents/com.sarahmaeve.toolbox-bridge.plist

# Load and verify
launchctl load ~/Library/LaunchAgents/com.sarahmaeve.toolbox-bridge.plist
toolbox-bridge doctor                              # daemon probe should now read PASS
launchctl list | grep toolbox-bridge               # confirm launchd has it
```

The plist runs `serve run` (foreground) — launchd is the supervisor, so we deliberately skip `serve start` and the PID file. `KeepAlive=true` restarts the bridge on crash; `RunAtLoad=true` starts it at login. To stop:

```bash
launchctl unload ~/Library/LaunchAgents/com.sarahmaeve.toolbox-bridge.plist
```

On Linux, the equivalent is a `~/.config/systemd/user/toolbox-bridge.service` unit with `ExecStart=$HOME/go/bin/toolbox-bridge serve run …` and `systemctl --user enable --now toolbox-bridge`. Recipe contributions welcome.

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
