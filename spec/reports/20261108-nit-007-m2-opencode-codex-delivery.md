# Session mailbox delivery for opencode and Codex

**Date:** 2026-11-08

NIT-007 M2 is in progress. This report records the approved work-queue run in the local macOS checkout at
`/Users/ripta/projects/nitipan`. Implementation and its checks passed. Independent review found two issues; fix round 1 passed. Row and final reviews remain pending.

## Run state

- Run: default `next 1`, NIT-007 M2 only. Milestone status: DONE; row complete; final review pending.
- Dependency: NIT-007 M1 is DONE. Proposal accepted; era 3. No skipped, held, or blocked rows.
- Code and specs share this repository. Base commit subject: `nitipan: say the pre-push hook trusts the atomic flag, and how to clear it`.
- Row base is the same as the run base. Run commit subjects: `nitipan: wake Codex and opencode sessions for pending mail`.
- Machine-local state tracks commit identifiers outside committed documentation.

## Approved plan and authorization

The user explicitly approved the complete plan on 2026-11-08. It authorizes this row through implementation,
independent milestone, row, and final reviews, proposal completion, and commits by explicit path on the checked-out
branch. It authorizes supported best guesses, review fixes, and path-specific stashing of unfinished blocked work.
It authorizes no push, pull request, branch switch, destructive git operation, or action on another session's edits.
Sandbox permissions remain separate.

1. Enable Codex and opencode mail and lifecycle registration. Adapt shared-host registration, routing, and liveness so
   each session receives its own rings. Preserve version compatibility and unreadable/newer-file protections.
2. Add Codex queue dispatch with executable and execution home from trusted LaunchAgent inputs. Registrations supply
   comparison data only. Missing configuration or home mismatch leaves mail unrung and explains the reason in status.
3. Extend the opencode server plugin with mailbox watching, presence, session.prompt rings, shutdown handling, and
   visible errors. Failed rings must retain mail and allow another attempt.
4. Add turn reminders through Codex hooks and the opencode plugin. Share the ring ledger; rings carry no message body.
   Only explicit mail read consumes content.
5. Extend isolated fake-provider live harnesses to prove idle wake followed by mail read. Record embedded Codex TUI
   behavior without a daemon. Update setup guidance and the proposal's As built block.

Expected areas: internal/mailbox, internal/node/mail.go, CLI mail/hooks/setup, internal/setup,
plugins/opencode/nitipan/index.ts, focused tests, scripts/live-checks/codex.py and opencode.py, and README.md.

## Verification plan

- `make ci`: fmt-check, lint, Linux vet, race tests, build.
- Focused package tests through `make test TEST_PKGS=... TEST_ARGS=...`.
- `make live-codex LIVE_CODEX_ARGS=--selftest` and both harness probes.
- Focused new mail scenarios through make live-codex and make live-opencode, with private agent configs and fake
  providers. These share nitipan's state directory and remove only artifacts they create.
- Proposal consistency and freeze checks before commits and reviews.
- Verify shared-host isolation, execution-input spoof resistance, home mismatch, failed and duplicate rings, presence
  failures, turn reminders, and no-rewrite rules for newer files.
- No test-cost target exists. No credential-dependent command or user-only check is identified for this milestone.

## Best guesses

- Extend existing isolated harnesses and fake providers instead of using user sessions or paid calls. This follows the
  existing live harnesses and the milestone's scripted-test requirement. A changed harness contract would require
  adapting the scripts and rerunning them.
- Keep the broker wire protocol unless a concrete implementation need appears. M1 already derives names for both
  agents. A contrary need requires a documented compatibility analysis.
- Make shared-host delivery independent per session. Adapter MultiSession behavior and the proposal's addressed-session
  wake requirements support this. Incorrect isolation would misroute rings and must be repaired before completion.
- Preserve non-waking fallback when trusted Codex configuration is absent or mismatched, as settled in Per-agent
  delivery adapters and ADR-07.

## Evidence and prerequisites

Planning commands, run in the checkout:

- `python3 /Users/ripta/.codex/coding-standards-current/scripts/proposals.py next --proposals-dir spec/proposals --count 1`
  selected NIT-007 M2 and returned an empty skipped list.
- `python3 /Users/ripta/.codex/coding-standards-current/scripts/proposals.py row --proposals-dir spec/proposals NIT-007 M2`
  returned ready=true, accepted, NOT STARTED, dependency NIT-007 M1 met, and live_after=null before work began.
- `python3 /Users/ripta/.codex/coding-standards-current/scripts/detect_era.py --specs-dir spec` returned era 3.
- `git status --short` returned no paths before this run's first edits.

Checks require the project Go toolchain, versioned lint wrapper, Python 3, and installed Codex and opencode binaries.
Applicable ADRs: 01, 04, 05, 06, 07, and 08. No new ADR candidate is currently identified.

## Reviews and results

Initial implementation checks passed. Independent milestone and ADR reviews completed; fix round 1 passed. Row and final reviews remain pending. No stash is recorded.

Final focused live evidence, inspected directly on 2026-11-08:

- `/tmp/nitipan-codex-mail-final.md`: Codex 0.167.0, macOS 26.6.2, run 46c44610. Totals: 11 PASS,
  0 FAIL, 0 ERROR, 0 SKIP, 1 INFO. Idle Codex woke and read the synthetic message. The embedded TUI observation
  was `socket before=False; queue exit=0; read=True; socket after=False`. Real model calls: 0. The harness reports
  real config/hook mtimes and managed daemon PIDs unchanged, and no leases left behind.
- `/tmp/nitipan-opencode-mail-final.md`: opencode 2.2.6, macOS 26.6.2, run 9b1eb842. Totals: 8 PASS,
  0 FAIL, 0 ERROR, 0 SKIP. The plugin published presence, woke an idle session, and the session read the synthetic
  message. Real model turns: 0. The harness reports no leases left behind.

These live tests prove the agent delivery/read boundaries; node process-boundary tests separately prove trusted
execution arguments and failure behavior. Temporary raw logs may not survive a machine cleanup, so this report
preserves the relevant results.

## Implementation checks and diagnosed failures

- `make ci` passed on the final source tree (execution session 93360): formatting, lint with zero issues, Linux vet,
  race tests, and build. CLI package 416.368 seconds; node 31.966 seconds.
- `make test TEST_PKGS=./internal/mailbox TEST_ARGS='-run TestSharedHostMailRegistrationsKeepConcurrentSessionsSeparate'`
  failed on the unfixed registration merge, then passed after the fix.
- `make test TEST_PKGS='./internal/mailbox ./internal/node ./internal/cli ./internal/setup' TEST_ARGS='-run Mail'`
  passed during implementation.
- `make test TEST_PKGS='./internal/cli ./internal/node ./internal/mailbox' TEST_ARGS='-run TestOpencodeMailPoll|TestCodexMailTurn|TestSharedHostMail|TestCodexQueue|TestCodexHomeMismatch'`
  passed on the final tree: CLI 1.316 seconds, node 1.222 seconds, mailbox 1.067 seconds.
- `make fmt`, `make lint`, `make live-codex LIVE_CODEX_ARGS=--selftest`, in-memory Python syntax compilation, and
  `git diff --check` passed. The project has no test-cost target.
- `env -u CODEX_THREAD_ID -u CODEX_SESSION_ID make live-codex 'LIVE_CODEX_ARGS=--only mail,mail-embedded --results /tmp/nitipan-codex-mail-final.md'`
  produced the final Codex live evidence above.
- `make live-opencode 'LIVE_OPENCODE_ARGS=--only mail --results /tmp/nitipan-opencode-mail-final.md'`
  produced the final opencode live evidence above.
- The coordinator's proposal check returned only the two expected post-review transitions: mark NIT-007 implemented
  and move its finished row to history. Freeze returned `[]`; the run base remains an ancestor of HEAD.

Failures were diagnosed before reruns:

- Codex's initial harness invocation rejected inherited session IDs. The isolated invocation removes those two values.
- Codex probe d354580e failed because this macOS Python has no os.waitid. The owned host PID 55904 was confirmed gone
  before retry. A libc fallback preserves non-reaping exit observation; corrected probe f67dfe79 passed, including
  unchanged real-home and daemon guards.
- opencode probe 89b60053 passed the server, shell, execution mark, TUI and presence checks, but failed tabs.open because
  tabs.enabled=false. The committed harness and plugin reproduced that failure in run 8f99b8e0. This installed-feature
  mismatch predates the work. Mail scenarios do not depend on tabs and pass. No claim is made that tabs work here.
- opencode mail run 1e1299d7 failed because session.prompt requires text rather than parts. The integration was fixed.
  The implementer mistakenly started corrected run a82df8eb before the first finished. Private processes/homes/sessions
  were separate, but a scratch results filename was shared. Both runs finished and their owned host/TUI processes were
  confirmed absent. Final runs used distinct result paths.
- CI 25503 stopped on a capitalized error string; fixed. Intermediate CI 97392 found a test assertion forbidding the
  substring other also matched ordinary another-session wording. The assertion now checks a unique synthetic body
  sentinel. Focused and full final checks passed.
- py_compile could not write the OS Python cache in the sandbox. Equivalent in-memory syntax compilation passed.

## Implementation best guesses and compatibility

- Keep Claude registrations process-keyed with pre-clear aliases. Key Codex/opencode registrations additionally by
  session and use their existing session liveness rules, preserving the foreign-machine guard before checking local process death. This implements independent
  addressed-session delivery on shared hosts.
- Registration and endpoint versions remain 1.0. Older CLIs refuse mail for these agents and older nodes never push to
  them. Codex home is comparison data. Pooled shared-host registrations read as unusable and are never guessed at or
  rewritten. ADR-04 unknown/newer-file protections remain required. Review will examine this compatibility choice.
- Hidden Go helpers own opencode registration, presence, liveness, and atomic ring claims. The plugin watches inboxes
  and polls known sessions in bounded batches. This keeps version and locking rules with their production owner.
- The live harness publishes synthetic mail at the durable local-output boundary. Node tests prove dispatch inputs,
  routing, claim/retry behavior, and retained content. Real-agent checks prove wake/read. They do not prove production
  broker enrollment or sender stamping. This split avoids duplicating broker proofs from M1.
- The harness gains --results for scratch evidence rather than overwriting historical reports. The macOS process
  observation repair was necessary for the required Codex live proof.
- No broker wire change, ADR edit, or new ADR candidate is recorded.

## Milestone review and fix round 1

The independent reviewer reported two findings:

1. CONFIRMED correctness: `internal/mailbox/registration.go:196` checks a local PID before the machine guard. A
   foreign-machine registration with no matching local PID can read dead, then be reaped or have unread mail expired.
   Disposition: fix. Restore foreign-machine unknown behavior and prove the regression before the fix.
2. Plan gap: `plugins/opencode/nitipan/index.ts:226` and `internal/cli/hook_plugin_mail.go:90` route all plugin rings
   through waking claims. Once claimed, unread mail has no independent non-waking turn reminder. Disposition: trace,
   then implement the approved reminder path if confirmed, with proof an already-rung message remains visible during
   later activity without issuing another wake.

Both findings were confirmed and repaired by the same implementer, within the approved plan:

- The foreign-machine regression failed before the fix. Live now checks machine identity before applying local
  process death; foreign registrations retain unknown liveness while genuinely dead local hosts remain dead.
- Live reminder regression ea482647 reached its first wake, ignored it, retained unread mail, and produced later
  model requests with `reminders=[False,False]`. The context hook now calls a separate non-claiming Go helper and adds
  ring-only `{text: ...}` objects to context.system. It never calls session.prompt or claims waking keys. The installed
  opencode provider adapter reads each system item's text field. Trial bfdc13d5 caught an initial string-shape mistake
  before the final implementation used the correct object shape.
- `/tmp/nitipan-opencode-reminder-final.md`, inspected directly: run a87cf7e4, opencode 2.2.6, macOS 26.6.2,
  14 PASS, 0 FAIL/ERROR/SKIP/INFO. First wake arrived; unread mail stayed durable; later turn and post-tool requests
  produced `requests=2; reminders=[True, True]`; no extra wake was enqueued; explicit read consumed the message. The
  ordinary idle wake/read scenario also passed. Real model turns: zero; fake-provider scripted turns: four.
- Owned host/TUI processes from all three reminder trials were confirmed absent.
- Final focused race tests passed (CLI 1.180 seconds, mailbox cached), lint returned zero issues, Python syntax and
  diff checks passed. Final CI execution session 91998 passed on the completed fixed tree: exit 0, CLI 426.275 seconds, mailbox 1.319 seconds, node 30.580 seconds, and setup 11.781 seconds.
- CI attempt 35579 stopped at a function-length lint finding. The helper's redundant explicit rollback loop was removed
  because its existing deferred rollback already covers encoding failures. No lint suppression was added.
ADR-impact independently found no conflicts and no uncovered decisions. It checked ADR-01, ADR-04, ADR-05, ADR-06,
ADR-07, and ADR-08. ADR-02 is superseded; ADR-03's lease-key rule is untouched. No load-bearing list exists.

## Edits and state left untouched

Two empty lock files have unknown ownership. They were left untouched, not attributed to the run:

- `/Users/ripta/.local/state/nitipan/mail/ring/6165ee75ede89ab8d199bed6.lock`
- `/Users/ripta/.local/state/nitipan/mail/ring/4fdbc060af6542e9025a6b40.lock`

Final owned host/TUI processes were confirmed absent. The reg, inbox, read, and wait mail directories were empty.
No other unrelated edit was reported. All implementer commands finished before the implementation report.

## Status

M2 is DONE in the proposal, with As built and amendments. The run remains IN PROGRESS through row and final reviews.
The proposal remains accepted and its queue row remains until the row review completes. The milestone is committed; fresh row code and ADR reviews are running.

## Fix round completion and recovery

Completion was confirmed on 2026-11-09. The original run date and filename remain 2026-11-08. The implementer hit a
usage limit before its final CI result reached the coordinator. The coordinator could not access the child command
session and checked process names before resuming the same implementer context. No Go test or lint process remained;
a single make test-package process belonged to another session and was left alone. The same implementer recovered
session 91998 with exit 0; no CI rerun or replacement agent was needed. Every child command finished before handoff.

`make test TEST_PKGS='./internal/mailbox ./internal/cli' TEST_ARGS='-run TestForeignRegistrationWithAnAbsentLocalPID|TestOpencodeMailPoll'`
passed, as did make lint with zero issues, Python syntax, and git diff --check. Final reminder proof command:

```sh
make live-opencode 'LIVE_OPENCODE_ARGS=--only mail,mail-reminder --results /tmp/nitipan-opencode-reminder-final.md'
```

Both milestone review findings are fixed. No finding was dismissed or left unresolved. The final file list matched
coordinator git status apart from this coordinator-owned report. Freeze returned no findings. Proposal check has only
the two completion transitions deliberately reserved until row review. No new ADR candidate was identified.

## Milestone commit and row review

The milestone and its review fixes landed together as `nitipan: wake Codex and opencode sessions for pending mail`.
Code and specs share that commit. The coordinator checked the subject against repository history and Forbidden Openers,
staged explicit paths, and committed explicit paths. Git status was clean afterward. The base remains an ancestor.
Freeze with the row's specs commit returned `[]`. Fresh code and ADR agents are reviewing the entire row range.

## Row review finding

The fresh row ADR review returned no conflicts and no uncovered decisions across ADR-01, 04, 05, 06, 07, and 08.
The row code reviewer found one confirmed correctness/plan issue at plugins/opencode/nitipan/index.ts:251:
initial session discovery runs only once. If its list call fails, or a live idle recipient sits beyond the first 200
results, that recipient may never enter the watched session set. Later polling does not recover discovery, and an
idle session emits no new lifecycle event. Disposition: fix. A fresh row implementer is repairing recoverable,
eventually complete discovery with bounded processing and a regression proving wake without user activity.

The row reviewer confirmed the prior foreign-machine liveness and non-waking reminder fixes. No finding was dismissed.
The row remains under review until its discovery fix and checks pass. Proposal completion remains pending.

## Row fix 1 results

The discovery finding is fixed. A fresh implementer reproduced both the failed-initial-list and beyond-first-200
cases before repair. The production plugin now requests one page every five seconds, follows the installed SDK's
cursor.next, and resets after the last page or an error. It retains known sessions through lookup errors, guards
concurrent discovery, and stops queued callbacks after teardown. The five-second traversal is a run best guess based
on the existing polling interval and the requirement for bounded, eventually complete idle delivery.

`make test TEST_PKGS=./internal/setup TEST_ARGS='-run TestOpencodeMailDiscoveryRecoversAndReachesLaterPages -count=1 -v'`
passed uncached in 1.189 seconds. The test loads the production plugin at its external SDK/process boundary, uses a
manual clock, and proves recovery, later-page delivery without session events, bounded discovery, reset, and teardown.
It explicitly skips when Node lacks TypeScript stripping; this adds no production dependency.

The Go wrapper now reads the .mjs fixture and sends it to Node through stdin so Go observes the test dependency.
A repeated command produced a cached pass. An intentional fixture-only unique error then caused the same command to
fail instead of reusing stale success. The fixture was restored in finally before the final checks.

```sh
make live-opencode 'LIVE_OPENCODE_ARGS=--only mail,mail-reminder --results /tmp/nitipan-opencode-discovery-final.md'
```

Run 8b6ad7dc (opencode 2.2.6, macOS 26.6.2) passed 14 checks with no failures/errors/skips and zero real model turns.
Owned server/TUI PIDs 45984, 45986, and 46252 were confirmed absent. Its real-agent proof complements the deterministic
SDK-boundary discovery test.

Initial CI session 1209 passed (CLI 422.922 seconds, setup 10.057 seconds). Final CI session 85507 passed after the
cache-tracking fix: formatting, lint with zero issues, Linux vet, race tests, and build. Unchanged packages cached;
setup took 8.871 seconds. Diff check passed; freeze returned no findings; proposal check has only the two deliberate
completion transitions. All implementer commands finished and its four-file list matches coordinator status apart
from this report. No new ADR candidate, unrelated edit, or blocker was reported.

## Row completion

The discovery fix landed as `nitipan: rediscover idle opencode mail recipients`. The row review finding is fixed;
no unresolved trace or new ADR candidate remains. On 2026-11-09 the coordinator marked NIT-007 implemented, updated
its index row, and moved NIT-007 M2 to the end of history with its cells unchanged. The other queue rows are outside
this next-one run and remain scheduled. Final whole-run code and ADR review follows the completion commit.

## Final review findings

Final ADR-impact found no conflicts or uncovered decisions across ADR-01, 04, 05, 06, 07, and 08. Final code review
confirmed prior liveness, context-reminder, and discovery repairs. No ADR file changed, so final ADR-file review had
no additions or edits to assess. Freeze returned no findings.

The final code reviewer found two confirmed correctness issues:

1. internal/cli/hook_plugin_mail.go:61: a session with an unusable registration causes the helper to return before
   healthy recipients; the plugin's outer catch also skips later batches. Stable iteration can starve idle recipients.
   Disposition: fix with per-recipient failure isolation, visible errors, and progress for healthy sessions.
2. plugins/opencode/nitipan/index.ts:242: if a prompt fails and its rollback helper also fails, the outer catch prevents
   later already-claimed rings from being attempted. Their ledger claims then suppress future wakes. Disposition: fix
   by continuing independent rings and retaining rollback work for retry.

A fresh final-review implementer is repairing both within the approved delivery contract, with pre-fix regression
proofs. The proposal is implemented; any As built correction follows frozen-proposal correction-of-fact rules.
No design change or new ADR is authorized by these findings.

## Final fix 1 results

Both final findings are fixed. Before repair, CLI tests for undecodable and higher-major registrations exited 1
before healthy recipients were reached. Production-plugin regressions also failed for a rejected first batch and for
a prompt failure followed by failed rollback. After repair, per-recipient diagnostics no longer suppress healthy
rings. The plugin isolates batch and prompt failures and retains failed rollback keys. Healthy deliveries run first;
one rollback helper is attempted per poll, with failed recipients rotating behind others. An additional fairness
fixture proves a permanently failing recipient cannot starve another recoverable recipient.

`make test TEST_PKGS='./internal/cli ./internal/setup' TEST_ARGS='-run Mail -count=1'` passed: CLI 6.170 seconds,
setup 1.400 seconds. Focused per-recipient and production-plugin regressions passed. Lint reported zero issues.
Final CI session 7518 passed formatting, lint, Linux vet, race tests, and build: CLI 427.019 seconds, setup 11.029
seconds. Diff check, proposal check, and freeze all passed with no findings. Every command finished before handoff;
the implementer's six-file list matched git status apart from this coordinator-owned report.

```sh
make live-opencode 'LIVE_OPENCODE_ARGS=--only mail,mail-reminder --results /tmp/nitipan-opencode-batch-final.md'
```

Run 46f6a85b on 2026-11-09 (opencode 2.2.6, macOS 26.6.2), inspected directly by the coordinator, passed 14 checks
with no failures/errors/skips and zero real model turns. Later turn and post-tool requests both carried the reminder,
no extra wake was enqueued, and only explicit read consumed mail. Owned server/TUI PIDs 88846, 88966, and 89356 were
confirmed absent. Initial attempt e9653b3b stopped before installation on a sandbox Go-cache denial; it launched no
test server. The successful trial used the command approval mechanism.

The implemented proposal received an As built correction with a Decision Log quoting the prior claim. No settled
design changed. Newer/unusable files remain untouched. No new ADR candidate, unresolved trace, unrelated edit, or
blocker remains. No finding was dismissed. Final ADR review found no conflict; no ADR file was added or edited.
