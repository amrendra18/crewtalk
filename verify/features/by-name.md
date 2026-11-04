# by-name

Use case: agents find each other by name, and a reply belongs to the right
conversation.

The journey starts two `crewtalk wait` processes, so both names join the
address book as Claude agents. The first sends to the second by name. The
second answers by name with `--reply-to` and the id of the first message. No
Codex session and no Claude session takes part. The run uses its own
temporary `CREWTALK_HOME`, so it needs no settings.

Proven when all of these hold:

1. Each listener prints the other's marker under a header that names the
   sender.
2. The reply's header carries the first message's thread, turn 2 and
   `reply-to` with the first message's id.
3. The journal holds a line with `reply_to` set to that id.
4. Each delivery takes under 0.1 s, from the time in the message id to the
   moment the listener prints the message.

Run it alone with `scripts/verify fast --feature by-name`.
