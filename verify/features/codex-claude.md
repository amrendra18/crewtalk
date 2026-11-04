# codex-claude

Use case: a Codex session asks a Claude session.

This is the reply leg of the `claude-codex` round trip. The Codex session runs
`crewtalk send --to claude:verify-NNN MARKER`, and the harness's listener
receives it on the Claude route.

Proven when all of these hold:

1. The listener prints the marker under a header that names `codex:THREAD`.
2. The journal holds 2 `delivered` lines for the listener.
3. The reply's delivery takes under 0.1 s, from the time in its message id to
   the moment the listener prints it.

Without `CREWTALK_VERIFY_CODEX_THREAD` the journey is skipped. Run it alone
with `scripts/verify fast --feature codex-claude`.
