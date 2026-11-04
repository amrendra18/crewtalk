# limits

Use case: a loop between agents stops, and `PAUSE` stops all sends.

The journey runs six turns of one thread between two `crewtalk wait`
listeners, each turn a `--reply-to` of the one before. Then, with a listener
running, it sends a seventh turn, and a new message while `CREWTALK_HOME/PAUSE`
exists. Then it removes `PAUSE` and sends once more. No Codex session and no
Claude session takes part. The run uses its own temporary `CREWTALK_HOME`, so
it needs no settings, and it unsets `HERDR_ENV`, so no operator notice is
shown.

Proven when all of these hold:

1. All six turns are delivered.
2. The seventh turn exits 3 and prints one line on stderr.
3. The send while `PAUSE` exists exits 3 and prints one line on stderr.
4. The listener prints only the message sent after `PAUSE` was removed.
5. The journal holds one `refused-limit` line and one `refused-paused` line.
6. Each delivery takes under 0.1 s, from the time in the message id to the
   moment the listener prints the message.

The limit of 20 sends per sender and receiver in one hour, and the operator
notices, are proven by the unit tests.

Run it alone with `scripts/verify fast --feature limits`.
