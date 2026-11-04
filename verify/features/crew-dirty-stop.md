# crew-dirty-stop

Use case: `crew stop` never loses work.

The journey starts a worker as `crew-fake-round` does. It then changes a tracked
file, adds an untracked file and adds an ignored file in the worker's folder, and
runs `crew stop`. A test input in `./scripts/check full`, a stand-in `git` that
reports a clean status and removes the folder, shows that this journey sees a lost
file.

Proven when all of these hold:

1. `crew stop` exits 0, and its output names the folder and 3 kept paths.
2. All 3 files still hold their text.
3. The branch stays.
4. The pane is closed, and the worker's name is gone from `crewtalk who`.

Run it alone with `scripts/verify fast --feature crew-dirty-stop`.
