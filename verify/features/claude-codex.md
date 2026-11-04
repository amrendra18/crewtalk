# claude-codex

Use case: a Claude session asks a Codex session.

The harness listens as `verify-NNN` with `crewtalk wait`, standing in for a
Claude session. It sends a request with `crewtalk send --to codex:THREAD` to
the Codex thread in `CREWTALK_VERIFY_CODEX_THREAD`. The request asks the Codex
session to run one reply command. One round trip proves this journey and
`codex-claude`.

Proven when all of these hold:

1. The listener prints the marker under a header that names `codex:THREAD`.
2. The journal holds 2 `delivered` lines for the listener.
3. The `codex queue` call (the request's `crewtalk send`) takes under 1.9 s.

Without `CREWTALK_VERIFY_CODEX_THREAD` the journey is skipped. Run it alone
with `scripts/verify fast --feature claude-codex`.
