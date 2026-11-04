# two-projects

Use case: one **"lead"** (a coordinating Claude session) gives instructions to two
Codex workers, one per project, and gets each reply. This is the part of that
scenario that a script can drive.

The journey joins two names as Codex agents, one per thread, and starts a
`crewtalk wait` listener that stands in for the lead. The lead sends one
instruction by name to each worker. Each worker runs
`crewtalk send --to lead --reply-to ID MARKER`. The listener exits after each
message, and the journey starts it again.

A fast run uses the fake codex with two fake thread ids. A full run uses the
real threads `CREWTALK_VERIFY_CODEX_THREAD` and
`CREWTALK_VERIFY_CODEX_THREAD_2`. The two must differ, or the run exits 64.

Proven when all of these hold:

1. The two instructions get different threads.
2. Both markers reach the lead, one per `crewtalk wait`.
3. Each reply's header names its worker's thread as the sender, and carries
   its instruction's thread, turn 2 and `reply-to` with that instruction's id.

When the run ends, it leaves no `verify-` name in the address book and no
`crewtalk wait` running. `./scripts/check full` fails otherwise.

Two test inputs must make this journey fail. `FAKE_CODEX_SILENT_THREAD` keeps
the fake from replying for one thread, so that worker's reply never arrives.
`./scripts/check full` runs it with a timeout of 2 s. A stand-in worker that
replies without `--reply-to` breaks point 3.

Without both settings the journey is skipped. A skipped journey makes
`scripts/verify` exit 1 in a `full` run, and in a `fast` run with
`--feature two-projects`. A `fast` run of every journey reports it skipped and
exits 0. With both settings and no stand-in, it contacts the two real threads.
To run it alone with the fake codex, from the repository folder:

```sh
CREWTALK_HOME=$(mktemp -d /tmp/crewtalk-verify.XXXXXX) \
  CREWTALK_CODEX_BIN=$PWD/internal/route/codex/testdata/codex FAKE_CODEX_MODE=reply \
  FAKE_CODEX_RUNS=/dev/null FAKE_CODEX_ARGS=/dev/null \
  CREWTALK_VERIFY_CODEX_THREAD=11111111-2222-4333-8444-555555555555 \
  CREWTALK_VERIFY_CODEX_THREAD_2=66666666-7777-4888-9999-000000000000 \
  scripts/verify fast --feature two-projects
```

The fake replies at once, so one worker can finish before the other gets its
instruction. Only the hand journey shows both workers working at once.

This journey does not prove that the end of `crewtalk wait` wakes a real Claude
Code session, or that a draft stays unsent. The hand journey proves those.
