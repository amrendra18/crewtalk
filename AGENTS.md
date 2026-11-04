# Agent rules

## Context

- Read `README.md` and the project's requirement, contribution, and design
  documents before changing behavior or public interfaces.

## Authority

- Git configuration, hook or dependency installation, staging, commits,
  remotes, publication, and external services require explicit
  authorization.
- Stage only approved paths; never use broad staging commands.
- Preserve unrelated work.

## Engineering

- Use simple, neutral, technical English. Remove filler and repetition;
  preserve requirements and security limits.
- Use `README.md` as the novice entry point. Present purpose,
  prerequisites, first-run and routine commands, expected results, and stop
  or recovery steps in execution order.
- Prefer the smallest complete design. Add complexity only for a
  demonstrated need.
- Make correct use easy and obvious. Use secure defaults, require opt-in for
  risky behavior, and give precise recovery guidance.
- Use red-green-refactor for behavior changes.
- Never commit secrets, private keys, or personal data.

## Invariants

Each invariant names its tests.

1. A message reaches the receiver's live session through that harness's own
   entry point. No reply is ever read off a screen. Test: the `scripts/verify`
   journeys `claude-codex`, `codex-claude` and `claude-claude`. In every
   journey, `crewtalk wait` processes stand in for Claude sessions.
   `claude-claude` runs two of them, and no session at all. Two things only
   the live journeys prove. `scripts/verify full` proves that a real Codex
   session gets the request and runs the reply. A journey by hand with real
   Claude Code sessions proves that the end of `crewtalk wait` wakes the
   session. `journey-peers`, by hand, in `verify/hand-journey.md`, runs two
   real Claude Code sessions, and two real Codex sessions.
2. crewtalk never types into an input box the operator may be using, and
   never answers an open dialog. Test: `journey-draft-safe` and
   `journey-busy` (a busy Codex session runs a message after its current
   turn), by hand, in `verify/hand-journey.md`.
3. Every message starts with a header that names the sender. A message never
   counts as the operator's approval. Test: `TestHeader*` in
   `internal/message`.
4. Each send writes exactly one line to `~/.crewtalk/log.jsonl`; each later
   change of state, such as `expired`, adds one more. `crewtalk log` prints
   one line for each. A send whose line cannot be written after delivery
   keeps its result and exit code, says so on stderr, and adds
   `"logged": false` to its `--json` output. Test: `TestLog*` in
   `cmd/crewtalk` and `internal/journal`; `TestScale` in `cmd/crewtalk`;
   `TestLogFailureAfterDeliveryIsReported` in `cmd/crewtalk`.
5. Loops stop. A send is refused with exit 3, reaches no route, and logs one
   `refused-limit` line when its turn is over 6, or when its sender already
   sent 20 messages to its receiver in the last hour, counted from the log.
   While `~/.crewtalk/PAUSE` exists, every send is refused the same way and
   logs `refused-paused`, and `crewtalk wait` exits 3 before it takes a held
   message or opens its socket. The message stays held and can still expire.
   A `crewtalk wait` already listening keeps listening, because no send
   reaches it; if a stalled connection makes it reopen its socket, it can
   print one held message. Each refusal prints one stderr line that says why
   and what happens next. Warnings about broken state files (a log or held
   line that is not JSON) may come before it, each starting with
   the command's name and `warning:`. A send that cannot lock and open the
   log sends nothing and exits 5. Two sends can still pass the count: sends
   from one sender to one receiver at the same instant, and a send whose log
   line fails after delivery. Test: `TestLimits*` and `TestPause*` in
   `cmd/crewtalk`.
6. A message to a Claude agent that is not listening is held in
   `~/.crewtalk/held/NAME.jsonl`, logged `held`, and the send exits 0, when
   the address book has an entry with that kind and address. Otherwise the
   send exits 4 with `not-listening`. When the hold step finds a listener
   after all, the send tries delivery once more instead. A line in a held
   file that is not JSON is skipped with a warning and kept. A command that
   rewrites the file can warn twice about that line, with different line
   numbers. The next `crewtalk wait --as NAME` prints the oldest held
   message, removes it after the print, and logs `delivered` with
   `held_for`. A listener checks
   for held messages under the held lock each time before it opens its
   socket, so no message stays held while its listener listens. A held
   message older than 30 minutes expires at the start of the next `send`,
   `wait` or `log`: it logs `expired`, the operator gets a notice, and the
   sender is told on its next send that is not refused. Test: `TestHeld*`
   and `TestExpiry*` in `cmd/crewtalk` and `internal/held`;
   `TestClaudeAbsent*`, `TestClaudePresentWhileListening` and
   `TestClaudeClaimWaitsOutAPresenceProbe` in `internal/route/claude`.
7. `crewtalk send` never branches on harness names. A new harness adds one
   route file and one route-table row for sending. `crewtalk wait` is the
   Claude route's listener and calls that route directly. Test:
   `TestRouteTableTakesANewKind` in `cmd/crewtalk`.
8. crewtalk needs at most one narrow, named permission per harness, and no
   broad sandbox change. `codex execpolicy check` against
   `~/.codex/rules/default.rules` must allow `crewtalk send` and must not allow
   `herdr agent prompt`, `PATH=/x crewtalk send` or
   `CREWTALK_HOME=/x crewtalk send`. `scripts/verify fast` skips this step and
   does not fail. A shell line that misses the rule, such as one with a backslash
   in its text, runs in the sandbox, exits 5 and sends nothing. Test: the
   `scripts/verify full` step `codex-rule`; `journey-quoting`, by hand, in
   `verify/hand-journey.md`.
9. crewtalk never retries a timed-out `codex queue` call on its own, and a
   `not-listening` Codex send makes exactly one queue call and holds nothing.
   Test: `TestQueueNoRetry` in `internal/route/codex`;
   `TestCodexNotListeningSendQueuesOnceAndHoldsNothing` in `cmd/crewtalk`.
10. Agents find each other by name. `crewtalk join`, `crewtalk wait` and
    `crewtalk leave` rewrite `~/.crewtalk/agents.json` whole under a lock, and
    a later join of a name replaces its entry. A send to a name nobody joined
    reaches no route and logs one `receiver-unknown` line. Claude names that
    differ only by case share one listener and one held file on a
    case-insensitive volume, so give them names that differ by more. Test:
    `TestJoin*` and `TestLeave*` in `cmd/crewtalk` and `internal/book`; `TestWho*` in
    `cmd/crewtalk`.
11. A reply stays in its conversation. `crewtalk send --reply-to ID` takes the
    thread of message ID and the next turn, and names ID in the header and
    the log line. Test: `TestThread*` in `cmd/crewtalk`; `TestHeader*` in
    `internal/message`.
12. `crewtalk wait` exits 0 only after it printed the message. A failed print,
    a closed standard output included, exits 5, and the sender is told
    `outcome-unknown`. Test: `TestWaitClosedStdoutExits5` and
    `TestCutOffSendIsNeverPrinted` in `cmd/crewtalk`.
13. The operator is told through a notice: `herdr notification show`, run
    only when `herdr` is on PATH and `HERDR_ENV=1`. A notice never fails a
    command. Every refused send and every expiry raises one. Test:
    `TestNotice*` in `internal/notice` and `cmd/crewtalk`.
14. One **"lead"** works with two Codex workers at once. Each instruction goes by
    joined name to its own thread. The lead restarts `crewtalk wait` after each message,
    and each reply uses `--reply-to`, so it keeps its own thread at turn 2. A
    run leaves no address book row and no running `crewtalk wait`. A worker whose
    reply never arrives fails the journey. The real threads need two different
    ids. Test: the `scripts/verify` journey `two-projects` for threads and turns;
    `journey-two-workers`, by hand, in `verify/hand-journey.md`, for "at once".

## Verification

- Run focused tests and the project's full check before you open a pull
  request.
- Report missing dependencies, residual risks, and checks that were not run.
