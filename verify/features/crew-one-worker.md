# crew-one-worker

Use case: a lead starts one real Codex worker, the worker replies, and the lead
stops it.

This journey spends model quota. It needs a Codex session that starts with no
dialog, so trust the repository in Codex once first. Run it inside herdr, from the
root of your clone, with a low-cost model:

```sh
CREW_VERIFY_MODEL=MODEL scripts/verify full --feature crew-one-worker
```

`scripts/verify fast` reports this journey as skipped. In `scripts/verify full`, a
missing `CREW_VERIFY_MODEL` or a run outside herdr skips it, and that fails the run.

A worker tab opens in your workspace, in a worktree under `CREW_HOME` (default
`~/.crew`). The row stays in `CREW_HOME/workers.json`, because rows are never
deleted. The journey adds one untracked file to that worktree before `crew stop`,
and removes the file, the folder and the branch itself after the check.

Proven when all of these hold:

1. `crew start` exits 0.
2. The lead prints the task's marker from a `codex:` thread, in the task's thread,
   at turn 2, with `reply-to` set to the task's id.
3. `crew stop` exits 0, the pane is closed, and the untracked file and the folder stay.
4. No worker name stays in `crewtalk who`, and no `crewtalk wait` for the worker
   still runs.
5. The checkout's `git status` is the same before and after.
