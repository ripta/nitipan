# NIT-001 M4: opencode live results (automated harness)

- Started 2026-10-23 01:33:52; run `a291a5eb`; work directory `/var/folders/k2/l3f2x3cn67n_qz_805l81zph0000gn/T/nitipan-live-a291a5eb`
- opencode opencode v2.1.0; macOS 26.6.2; private server, fake provider, no real model
- Rows are written as each check finishes. A run that stops early leaves the rows so far, and no totals.

| Handoff step | As built row | Check | Result | Detail |
|---|---|---|---|---|
| 0 | Plugins shipped, `--install-plugins`, and printed steps | make build | PASS |  |
| 0 | Plugins shipped, `--install-plugins`, and printed steps | setup opencode --install-plugins (private config) | PASS | -a291a5eb/xdg/config/opencode/plugins/nitipan/package.json wrote /var/folders/k2/l3f2x3cn67n_qz_805l81zph0000gn/T/nitipan-live-a291a5eb/xdg/config/opencode/plugins/nitipan/tui.ts  Restart the opencode service and each TUI so they load the plugins. nitipan setup opencode prints the rest of the steps. |
| 0 | Plugins shipped, `--install-plugins`, and printed steps | private opencode serve starts | PASS | port 51296 |
| 0 | Plugins shipped, `--install-plugins`, and printed steps | plugin files installed | PASS | ['index.ts', 'package.json', 'tui.ts'] |
| 13 | Plugins shipped, `--install-plugins`, and printed steps | plugin inventory from the API | INFO | 200 {"location": {"directory": "/private/var/folders/k2/l3f2x3cn67n_qz_805l81zph0000gn/T/nitipan-live-a291a5eb"}, "data": []} |
| 13 | Plugins shipped, `--install-plugins`, and printed steps | server plugin loaded: its execution mark appears under the server PID | PASS | mark pids [27769], server 27769 |
| 2 | `acquire` with no TUI fails and says why | acquire with no TUI on the session fails and names the TUI plugin | PASS | acquire a lease, because nothing marks it as open. |
| 10 | Presence ties to the TUI, `run` and `acp` safe | opencode run session gets no presence mark | PASS | presence pids [] |
| 10 | Presence ties to the TUI, `run` and `acp` safe | opencode run turn has an execution mark under the server | PASS | mark pids [27769], server 27769 |
| 8 | Server plugin sees events, including a resumed turn | execution mark removed when the opencode run turn ends | PASS |  |
| 2 | `acquire` with no TUI fails and says why | acquire inside opencode run fails with no TUI | PASS | exit=0 'nitipan: opencode session ses_e981ac117ffeGka2UXKEks6303 has no presence mark from a live TUI. It needs a TUI with nitipan\'s plugin loaded. A session from opencode run, the desktop app, or the API cannot acquire.\n\nopencode needs nitipan\'s two plugins before a session can acquire a lease. |
| 10 | Presence ties to the TUI, `run` and `acp` safe | opencode acp session gets no presence mark | PASS | presence pids [] |
| 10 | Presence ties to the TUI, `run` and `acp` safe | acp turn has an execution mark from its own server | PASS | mark pids [28867], server 27769 |
| 8 | Sweep keeps what it must and removes dead files | acp execution mark removed after the turn | PASS |  |
| 13 | TUI plugin marks follow its tabs and route, survive reload | TUI renders under a pty and its plugin marks the session | PASS |  |
| 1 | opencode session ID and PID in a bash tool call | OPENCODE_SESSION_ID in the shell tool is the session | PASS | ses_e981a6c0effe54g8LiQEzlU15q |
| 1 | opencode session ID and PID in a bash tool call | holder.session_id is the session | PASS | ses_e981a6c0effe54g8LiQEzlU15q |
| 1 | opencode session ID and PID in a bash tool call | holder.pid is the server | PASS | 27769: opencode serve --hostname 127.0.0.1 --port 51296 |
| 1 | Liveness sysctl inside each tool | liveness alive from this TUI's presence mark | PASS | {"status": "alive", "reason": "presence mark from TUI 29427"} |
| 2 | `acquire` with and without the printed entries | no permission prompt was needed | PASS | the scripted turn ran unattended |
| 2 | `acquire` with no TUI fails and says why | acquire with no TUI on the session fails and names the TUI plugin | PASS | acquire a lease, because nothing marks it as open. |
| 12 | Permission mode lists | --permission-mode build succeeds | PASS | exit=0 'acquired\n01M5KYB8PQ2T6JA20MM3TBVVJB  alive\n  domain: /var/folders/k2/l3f2x3cn67n_qz_805l81zph0000gn/T/nitipan-live-a291a5eb/checkouts/perm\n  handle: point-myself-twin-response\n  agent name: (none)\n  holder: opencode session ses_e981a6c0effe54g8LiQEzlU15q, pid 27769\n  permission mode: b |
| 12 | Permission mode lists | --permission-mode nonsense fails listing build, plan, auto | PASS | exit=2 'nitipan: permission mode "nonsense" is not one opencode offers; accepted values: build, plan, auto' |
| 12 | Permission mode lists | a mode on one acquire applies to the session's other leases as designed | PASS | None -> build |
| 12 | Permission mode lists | opencode primary agents | INFO | ['build', 'plan']; plus the --auto flag |
| 11 | TUI plugin marks follow its tabs and route, survive reload | child session's parent is the root | PASS |  |
| 11 | TUI plugin marks follow its tabs and route, survive reload | child marked present under the root's TUI | PASS | marks for child: [29427] |
| 11 | TUI plugin marks follow its tabs and route, survive reload | child is its own holder | PASS | exit=0 'acquired\n01M5KYBAFBHKY1Z6321TK9WJH1  alive\n  domain: /var/folders/k2/l3f2x3cn67n_qz_805l81zph0000gn/T/nitipan-live-a291a5eb/checkouts/sub\n  handle: arctic-install-cricket-swift\n  agent name: (none)\n  holder: opencode session ses_e981a5841ffexpq5K7mgvn7rdw, pid 27769\n  description: harn |
| 3 | MCP variable passing, and opencode sessions per process | two sessions share one holder PID | PASS | 27769 / 27769 |
| 5 | TUI plugin marks follow its tabs and route, survive reload | tab API | INFO | tabs enabled: True; route {'type': 'session', 'sessionID': 'ses_e981a522dffeuR064DNZuDof3N'} |
| 5 | TUI plugin marks follow its tabs and route, survive reload | a new tab's session is marked | PASS |  |
| 5 | TUI plugin marks follow its tabs and route, survive reload | an idle tab still holds its mark | PASS |  |
| 5 | TUI plugin marks follow its tabs and route, survive reload | closing the tab removes the mark | PASS |  |
| 5 | TUI plugin marks follow its tabs and route, survive reload | route without a tab | SKIP | the v2.1.0 TUI opens every session in a tab |
| 8 | Server plugin sees events, including a resumed turn | a TUI session's turn gets an execution mark under the server | PASS |  |
| 5 | TUI plugin marks follow its tabs and route, survive reload | a running session loses its TUI mark when its tab closes | PASS | running state is not a source |
| 5 | TUI plugin marks follow its tabs and route, survive reload | the server's execution mark outlives the tab | PASS | [27769] |
| 5 | TUI plugin marks follow its tabs and route, survive reload | TUIs without a tab for a session do not mark it | PASS | marked by TUIs [] |
| 8 | Server plugin sees events, including a resumed turn | the execution mark goes within 10 s of the turn ending | PASS |  |
| 6 | TUI plugin marks follow its tabs and route, survive reload | marks and leases survive a hot reload | PASS |  |
| 6 | Plugins shipped, `--install-plugins`, and printed steps | setup --replace restores tui.ts | PASS |  |
| 7 | Presence ties to the TUI, `run` and `acp` safe | holder.pid is the private serve child of the TUI | PASS | 32291: ppid 32290 /opt/homebrew/Cellar/opencode/2.1.0/bin/opencode serve --stdio --port 0 |
| 7 | Presence ties to the TUI, `run` and `acp` safe | presence mark names the TUI, not the child | PASS | [32290] vs TUI 32290 |
| 7 | Server plugin sees events, including a resumed turn | execution mark names the serve child | PASS | [32291] |
| 7 | Server plugin sees events, including a resumed turn | execution mark goes when the turn ends | PASS |  |
| 8 | Sweep keeps what it must and removes dead files | no stale execution mark after a standalone TUI exits | PASS | [] |
| 9.2 | TUI dies mid-turn, `time_suspended`, bad DB | reads alive while the turn runs after its TUI died | PASS | [{"status": "alive", "reason": "execution mark under host 27769"}] |
| 9.2 | TUI dies mid-turn, `time_suspended`, bad DB | liveness reason while the turn ran | INFO | execution mark under host 27769 |
| 9.3 | TUI dies mid-turn, `time_suspended`, bad DB | reads dead within 15 s of the turn ending | PASS | {"status": "dead", "reason": "no presence mark from a live TUI and no execution mark"} |
| 8.3 | Server plugin sees events, including a resumed turn | the server restarts on the same port and data | PASS | 27769 -> 35588; started with --service |
| 8.3 | Server plugin sees events, including a resumed turn | execution marks seen after the restart | INFO | [35588]; new server 35588 |
| 8.3 | Server plugin sees events, including a resumed turn | the turn resumes after the restart | PASS |  |
| 8.3 | Server plugin sees events, including a resumed turn | the resumed turn has an execution mark under the new server | PASS | mark pids seen [35588], new server 35588 |
| 9.4 | TUI dies mid-turn, `time_suspended`, bad DB | reads unknown while time_suspended is set | SKIP | skipped: nitipan reads the real opencode.db path, which a private run cannot reach; covered by unit tests |
| 9.5 | TUI dies mid-turn, `time_suspended`, bad DB | an unreadable database reads unknown with a hint naming it | SKIP | skipped: same reason, and the real database is never touched |

Totals: 47 PASS, 0 FAIL, 0 ERROR, 3 SKIP, 5 INFO.
Real model turns: 0. Scripted turns answered by the fake provider: 21.
Leases at start: ['01M5KKN8VTPQNB7B08GV5Q7PDD']. Left behind by this run: none.
Raw events: `/var/folders/k2/l3f2x3cn67n_qz_805l81zph0000gn/T/nitipan-live-a291a5eb/events.jsonl`. TUI transcripts and command output are beside it.
