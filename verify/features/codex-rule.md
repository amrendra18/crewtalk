# codex-rule

Use case: a Codex worker can reply through `crewtalk send` and has no rule that
lets it type into another pane.

This step runs the real `codex execpolicy check` against
`~/.codex/rules/default.rules`. It only reads that file and contacts no session.

Proven when all of these hold:

1. The check gives `crewtalk send` the decision `allow`.
2. The check does not give `herdr agent prompt` the decision `allow`. With no
   matching rule, the check prints no decision, and Codex's approval settings
   decide.
3. The check does not give `PATH=/x crewtalk send` or
   `CREWTALK_HOME=/x crewtalk send` the decision `allow`. The step passes each
   word as its own argument. If either got `allow`, a sandboxed session could
   run its own program, or skip `PAUSE`, the limits and the log.

The check reads a command as a list of words. It does not parse a shell line
the way a live session does, so this step does not prove what a session does
with a shell line that sets a variable first.

`scripts/verify full` runs it. `scripts/verify fast` reports it skipped and
does not fail. Run it alone with `scripts/verify full --feature codex-rule`.

Four test inputs must make it fail. `./scripts/check full` puts a stand-in
`codex` first on PATH that answers with no decision for every command, then one
that answers `allow` for every command, then one that allows `crewtalk send` and
`PATH=/x crewtalk send`, then one that allows `crewtalk send` and
`CREWTALK_HOME=/x crewtalk send`. A stand-in that allows only `crewtalk send` must
get this step proven.
