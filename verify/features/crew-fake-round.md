# crew-fake-round

Use case: a lead starts one worker, the worker replies, and the lead stops it.

The journey runs the real `crew` and a real `crewtalk`, built with the test tag.
A fake `herdr` (`cmd/crew/testdata/herdr`) and a fake worker
(`cmd/crew/testdata/worker`) come first on PATH, and the fake codex
(`internal/route/codex/testdata/codex`) answers the task. A `crewtalk wait`
process stands in for the lead. The run uses a temporary repository, `CREW_HOME`
and `CREWTALK_HOME`, so it needs no settings and contacts no session.

Proven when all of these hold:

1. `crew start` exits 0 and prints the worker's id and the task's id.
2. The lead prints a reply from the fake thread, in the task's thread, at turn 2,
   with `reply-to` set to the task's id.
3. `crew stop` exits 0.
4. The clean folder and the branch are gone, no pane stays open, and the row reads
   `stopped`.
5. No worker name stays in `crewtalk who`, and no `crewtalk wait` for the worker
   still runs.

Run it alone with `scripts/verify fast --feature crew-fake-round`.
