# claude-claude

Use case: a Claude session messages another Claude session.

The journey starts two `crewtalk wait` processes that stand in for Claude
sessions. Each sends the other one message with `crewtalk send`, on the Claude
route. No Codex session and no Claude session takes part, in `fast` and in
`full`. The run uses its own temporary `CREWTALK_HOME`, so it needs no
settings.

This journey proves the Claude route between two listeners. It does not prove
that the end of `crewtalk wait` wakes a real Claude session. Only a live
journey by hand with real Claude Code sessions proves that.

Proven when all of these hold:

1. Each listener prints the other's marker under a header that names the
   sender.
2. The journal holds 2 `delivered` lines.
3. Each delivery takes under 0.1 s, from the time in the message id to the
   moment the listener prints the message.

Run it alone with `scripts/verify fast --feature claude-claude`.
