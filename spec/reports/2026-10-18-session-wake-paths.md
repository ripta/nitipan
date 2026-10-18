# Session Wake Paths

**Date:** 2026-10-18

This is a research write-up for NIT-007. Its claims are unproven unless marked. A research agent read docs, `--help`
output, and binary strings. It connected to no socket. The versions checked were Claude Code 2.1.302, opencode 2.0.32,
and Codex 0.163.0.

Each claim is marked VERIFIED, meaning primary evidence was seen, DOCUMENTED, meaning official docs say so, or
UNVERIFIED.

## Claude Code: the session inbox socket

DOCUMENTED. The cross-session messaging docs have a section on the session's inbox socket. It is explicitly for "a
script or hook to post into a session." When the receiving session is idle, Claude Code starts a new turn with the
message. The socket is limited to the OS user. Same-machine messaging works on every provider, and with feature-flag
fetching off.

DOCUMENTED. An optional first line, `{"type":"auth","token":...}`, carries `CLAUDE_CODE_MESSAGING_TOKEN` and proves the
sender is the session's own child. A connection closes if no complete line arrives within 30 seconds. The docs say the
variables are exported to hooks and Bash commands.

VERIFIED from binary strings. The message body is newline-delimited JSON of `type` `user`, with content, sender, message
ID, priority, and attachments. Only the auth line is documented, so the body format may change.

DOCUMENTED. A sender that is not the session's own child, such as a node daemon, is subject to the receiver's
`crossSessionInbound` setting. A prompting session delivers. A session that bypasses permissions holds the message for
approval. A `-p` session holds it for five minutes by default, then drops it. Unattended delivery needs
`crossSessionInbound: accept`. An admin can set `refuse` for a whole org.

## Claude Code: channels

DOCUMENTED. A stdio MCP server can declare the `claude/channel` capability and send `notifications/claude/channel`. The
model receives the content. The feature is a research preview. It needs claude.ai or Console API-key auth, and is not
available on Bedrock, Vertex, or Foundry. Team and Enterprise orgs need an admin to set `channelsEnabled`. Each session
needs `--channels`. A custom server also needs `--dangerously-load-development-channels`, which `-p` and the Agent SDK
ignore. Unattended use of a custom channel needs an admin to add it to `allowedChannelPlugins`. Remote Control is not
required.

VERIFIED from binary strings. A channel event joins the same queue as cross-session messages, so it likely starts a
turn on an idle session. That is an inference from code.

DOCUMENTED and VERIFIED. A server that negotiates MCP revision 2026-07-28 is not registered as a channel.
`MCP_PROTOCOL_NEGOTIATION=legacy` works around it.

UNVERIFIED. Channels may depend on fetched feature flags. Strings in the binary suggest a fetched gate. If so, settings
that turn off nonessential traffic would turn off channels.

Plain MCP notifications do not reach the model. `list_changed` only refreshes lists, which is DOCUMENTED. No handler
for `notifications/message` or `resources/updated` feeds the model, which is UNVERIFIED but likely.

## Claude Code: hooks

DOCUMENTED. SessionStart, UserPromptSubmit, PreToolUse, PostToolUse, PostToolUseFailure, PostToolBatch, and Stop can
return `additionalContext`. That gives a busy session a delivery path. An async hook with `asyncRewake` that exits with
code 2 wakes an idle session. Its timeout still applies, so a long-poll hook must re-arm.

## opencode

VERIFIED from `--help` and binary strings. The TUI uses a shared background service by default. That service exposes
`/api/session/:sessionID/prompt`, described as "durably admit one session input and schedule agent-loop execution."

DOCUMENTED. The server docs list `POST /session/:id/prompt_async`, with basic auth through `OPENCODE_SERVER_PASSWORD`.
Those docs describe `serve` as a separate server, which looks stale for 2.0.32.

UNVERIFIED. Whether MCP notifications reach the model.

## Codex

VERIFIED from `--help`. `codex queue --thread <id> --message <text>` posts to an existing session on the shared local
app-server. Whether that starts a turn on an idle thread is UNVERIFIED.

DOCUMENTED. The app-server offers `turn/start`, `turn/steer`, and `thread/inject_items` over stdio, a Unix socket, or
WebSocket. The docs call it experimental and not supported for production. `codex remote-control` is a separate command
that the user's Remote Control ban may cover.

VERIFIED. MCP notifications are logged, not given to the model. That they never reach the model is UNVERIFIED.

## Where messages enter a machine

- A per-session stdio MCP server, using channels. It wakes an idle Claude Code session, but only there. It inherits
  every channel gate. Each session holds its own broker connection.
- The NIT-004 node alone. It reaches all three agents: Claude Code through the inbox socket, opencode through its HTTP
  service, and Codex through `codex queue` or the app-server. One broker connection per machine. It is not a session's
  own child, so Claude Code needs `crossSessionInbound: accept`. It also depends on the undocumented socket body.
- Both, with the node relaying to per-session MCP servers. An untested variant has the MCP server, as a child of the
  session, post to its own session's socket. That would pass the own-child check without the channel allowlist.
  Whether MCP server processes receive `CLAUDE_CODE_MESSAGING_SOCKET` is UNVERIFIED.

## Claims that would most change NIT-007

1. Whether channels depend on fetched feature flags, which the work environment may turn off
2. Whether a sender that is not a child starts a turn on an idle session once `crossSessionInbound: accept` is set
3. Whether the work environment runs Claude Code on Bedrock, Vertex, or Foundry, or in an org that will not enable
   channels
4. Whether the socket body format stays stable
5. Whether MCP server processes receive `CLAUDE_CODE_MESSAGING_SOCKET`
6. Whether `codex queue` and opencode's prompt endpoint wake an idle session

## Sources

- [Claude Code channels](https://code.claude.com/docs/en/channels) and its
  [reference](https://code.claude.com/docs/en/channels-reference)
- [Claude Code cross-session messaging](https://code.claude.com/docs/en/cross-session-messaging)
- [Claude Code hooks](https://code.claude.com/docs/en/hooks)
- [Claude Code MCP](https://code.claude.com/docs/en/mcp)
- [opencode server](https://opencode.ai/docs/server)
- [Codex app-server](https://learn.chatgpt.com/docs/app-server)
