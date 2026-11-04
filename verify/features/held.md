# held

Use case: a Claude session that is not listening gets the message when it
listens again.

The journey joins a name as a Claude agent with no `crewtalk wait` running,
and sends to that name. Then it starts `crewtalk wait` for that name, and then
a second one with a 1 s timeout. No Codex session and no Claude session takes
part. The run uses its own temporary `CREWTALK_HOME`, so it needs no settings.

Proven when all of these hold:

1. The send exits 0 and its `--json` result is `held`.
2. The first `crewtalk wait` exits 0 and prints the marker under a header that
   names the sender and the message id.
3. The second `crewtalk wait` exits 4, so the message was handed over once.
4. The journal holds, for that message id, one `held` line and then one
   `delivered` line with `held_for`.

The 30-minute expiry is not part of this journey. The unit tests prove it
with messages that were held more than 30 minutes ago.

Run it alone with `scripts/verify fast --feature held`.
