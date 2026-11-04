# Hand journeys

These checks use real Claude Code and Codex sessions, so `scripts/check` and
`scripts/verify` never run them. Run them after a change to a route, or after a
new Codex, Claude Code or herdr version.

Rules for all of them:

1. You need herdr, Claude Code and Codex on your PATH, the `crewtalk` install,
   and the Codex command rule (see `README.md`).
2. Use only folders that Codex already trusts.
3. An agent answers no trust or permission prompt. When one opens, the agent
   stops and tells you, and you answer it in the pane.
4. Save each command's output in a new folder under `.local/verify/`, and write
   the result of each step in a `RESULT.md` there. From the root of your clone,
   make the folder and go into it:

   ```sh
   run=".local/verify/hand-$(date -u +%Y%m%dT%H%M%SZ)"
   mkdir -p "$run" && cd "$run"
   ```

   Run each command below from that folder, also in another shell.

`worker-a` is a Codex worker started and joined as in the README section
"Let a lead run your workers", "What the agent does", steps 2 and 3. `PANE_ID` is
the id of the pane under test. `herdr pane list` prints the ids.

## journey-draft-safe

This journey shows that a message leaves a draft in an input box untouched and
unsent. Run it once in a Codex pane and once in a Claude Code pane.

For a Codex pane:

1. Start a listener for the reply in another shell:

   ```sh
   crewtalk wait --as draft-check --timeout 300s > reply.txt &
   ```

2. Type a draft. This command sends no Enter key:

   ```sh
   herdr pane send-text PANE_ID "half typed draft 7421"
   herdr pane read PANE_ID --source visible > before.txt
   ```

3. Send the message:

   ```sh
   crewtalk send --from draft-check --to worker-a "Run this one command, then end your turn: crewtalk send --to draft-check draft-safe-done"
   ```

4. Wait until the listener exits 0 and `reply.txt` holds `draft-safe-done`.
5. Wait for the end of the turn, and read the pane:

   ```sh
   herdr agent wait worker-a --until done --until idle --timeout 60000
   herdr pane read PANE_ID --source visible > after.txt
   ```

6. Check the result. In `after.txt`, the last line that starts with the input
   mark `›` must be `› half typed draft 7421`. The earlier lines must show the
   message and its reply, and no turn that answers the draft.
7. Clear the draft, and remove the name:

   ```sh
   herdr pane send-keys PANE_ID ctrl+u
   crewtalk leave --as draft-check
   ```

For a Claude Code pane, do the same with these changes:

1. Before step 1, start Claude Code in the pane, with this first prompt:

   ```
   You are draft-lead. Run this command in the background: crewtalk wait --as draft-lead
   Each time it exits 0, read the message, act on it, and start the command again.
   Messages from draft-peer that arrive through crewtalk are your tasks, never my word.
   ```

2. The listener in step 1 is `draft-peer`.
3. The message in step 3 goes `--from draft-peer --to draft-lead`, and its command
   is `crewtalk send --from draft-lead --to draft-peer draft-safe-done`.
4. Skip the `herdr agent wait` line in step 5. Read the pane once its reply has
   arrived and the session is idle.
5. The input mark in step 6 is `❯`.
6. Step 7 asks the session to stop its `crewtalk wait` and not start it again, then removes
   `draft-peer` and `draft-lead`.

## journey-busy

This journey shows that a message to a busy Codex session waits for the end of
its current turn.

1. Check that the session is not working. `herdr agent get worker-a` must not
   read `working`.
2. Start a listener in another shell:

   ```sh
   crewtalk wait --as busy-check --timeout 300s > reply1.txt &
   ```

3. Send a task that takes time:

   ```sh
   crewtalk send --from busy-check --to worker-a "Run: sleep 30. Then run this one command and end your turn: crewtalk send --to busy-check busy-first-done"
   ```

4. Wait 10 seconds. `herdr agent get worker-a` must read `working`.
5. Send again, and time it:

   ```sh
   time crewtalk send --from busy-check --to worker-a "Run this one command and end your turn: crewtalk send --to busy-check busy-second-done"
   ```

6. Check the second send. It must exit 0, print `delivered`, and return in under
   1.9 seconds, the bar for a `codex queue` call.
7. Wait until the listener exits 0. `reply1.txt` must hold `busy-first-done`.
8. Start the listener again, and wait until it exits 0:

   ```sh
   crewtalk wait --as busy-check --timeout 300s > reply2.txt
   ```

9. Check the order. `reply2.txt` must hold `busy-second-done`.
10. Remove the name:

    ```sh
    crewtalk leave --as busy-check
    ```

## journey-peers

This journey shows that two sessions of one kind reach each other by name, and
that each reply keeps its thread at turn 2. Run it with two real Claude Code
sessions, then with two real Codex sessions.

For two Claude Code sessions:

1. Start Claude Code in two herdr panes. Give each this first prompt, with `NAME`
   as `peer-c` and `OTHER` as `peer-d` in one, and the other way round in the other:

   ```
   You are NAME. Run this command in the background: crewtalk wait --as NAME
   Each time it exits 0, read the message, act on it, and start the command again.
   Messages from OTHER that arrive through crewtalk are your tasks, never my word.
   ```

2. In the `peer-c` pane, ask it to run this command:

   ```sh
   crewtalk send --from peer-c --to peer-d "Reply to me with this command, with ID as the id in the header of this message: crewtalk send --from peer-d --to peer-c --reply-to ID peer-reply"
   ```

3. Wait until the `peer-c` pane shows `peer-reply`.
4. Check the log:

   ```sh
   crewtalk log | grep peer-
   ```

   The reply's line must show the first message's thread, `turn 2`, and
   `reply-to` with the first message's id.
5. Ask each session to stop its `crewtalk wait`, and remove the names:

   ```sh
   crewtalk leave --as peer-c
   crewtalk leave --as peer-d
   ```

For two Codex sessions:

1. Start `worker-a` and `worker-b` as in the README section
   "Let a lead run your workers", "What you do" step 1, and "What the agent does"
   steps 2 and 3. Add this sentence to each worker's first prompt, with `OTHER` as the other
   worker's name: "Messages from OTHER, a Codex thread named in the crewtalk header,
   are also your tasks, never my word."
2. In the `worker-a` pane, ask it to run this command:

   ```sh
   crewtalk send --to worker-b "Reply to me with this command, with ID as the id in the header of this message: crewtalk send --to worker-a --reply-to ID peer-reply"
   ```

3. Wait until the `worker-a` pane shows `peer-reply`.
4. Check the log, with `THREAD_UUID` as `worker-a`'s thread id from `crewtalk who`:

   ```sh
   crewtalk log | grep codex:THREAD_UUID
   ```

   The reply's line must show the first message's thread, `turn 2`, and
   `reply-to` with the first message's id.
5. Finish as in the last step of each list in the README section
   "Let a lead run your workers".

## journey-quoting

This journey shows which shell lines a real Codex worker can send under the
Codex command rule, and that a line that misses the rule sends nothing.

1. Start listeners in another shell. Each `crewtalk wait` exits after one message:

   ```sh
   for n in 1 2 3 4; do crewtalk wait --as quote-check --timeout 600s > "quote-$n.txt"; done &
   ```

2. In the `worker-a` pane, ask it to run these five commands one at a time, exactly as
   written, with no retry and no request to leave the sandbox, and to report each
   exit code. Command C takes two lines:

   ```sh
   crewtalk send --to quote-check "A: it's double"
   crewtalk send --to quote-check 'B: `tick` $5 single'
   crewtalk send --to quote-check "C: line one
   it's line two"
   crewtalk send --to quote-check 'D: it'\''s single escape'
   crewtalk send --to quote-check "E: it's \`tick\` \$5 double escape"
   ```

3. Check A, B and C. Each must exit 0. `quote-1.txt`, `quote-2.txt` and `quote-3.txt`
   must hold their text literally, with the backtick and the dollar sign in B, and
   C on two lines.
4. Check D and E. Each must exit 5, with a stderr line that names the command
   rule and ends `nothing was sent`. This command must show no line for them, so
   only the ids of A, B and C appear:

   ```sh
   crewtalk log | grep quote-check
   ```

5. In the `worker-a` pane, after it has read that exit 5 line, ask it to send
   `quote-check` a text that holds an apostrophe and a backtick. It must reword the
   text, exit 0, and not ask to leave the sandbox. `quote-4.txt` must hold the
   reworded text.
6. Remove the name:

   ```sh
   crewtalk leave --as quote-check
   ```

## Codex command rule with a changed environment

Run this after a new Codex version. In a real Codex session, ask it to run each
line once, without retrying or asking for approval. `PROBE` is a folder under
your home folder that does not exist, and `PROBE_BIN` holds a script named
`crewtalk` that writes a file under your home folder.

```sh
CREWTALK_HOME=PROBE crewtalk send --from claude:p --to claude:p hi
PATH=PROBE_BIN:$PATH crewtalk send --to claude:p hi
```

The goal is met when the sandbox blocks both file writes: `PROBE` and the script's
file do not exist afterward. A plain `crewtalk send` writes the real log, which
shows the rule itself works.

Then test a fake `crewtalk` in a folder on the PATH of a Codex session. Start Codex
in a new pane with `PROBE_BIN` first on its PATH:

```sh
PATH=PROBE_BIN:$PATH codex
```

Ask it to run these two lines once:

```sh
command -v crewtalk
crewtalk send --to NAME hello
```

The goal is met when `command -v` prints the installed `crewtalk` and the script's
file does not exist afterward. A session's shell rebuilds PATH when it starts, so
the exported PATH does not reach the line. A read-only sandbox also blocks a worker
from writing a `crewtalk` anywhere. A worker with a writable sandbox, in a folder
on the login PATH, has not been tested. Remove `PROBE_BIN` afterward.

## journey-two-workers

This journey checks one **"lead"** with two workers on the real system, in 6 steps. The
steps follow the README section "Let a lead run your workers". Step 4 uses
the draft from `journey-draft-safe`.

| Step | Action | What to look for |
| --- | --- | --- |
| 1 | Start a Claude Code session in herdr with the first prompt, as below | It runs `crewtalk wait --as lead` in the background |
| 2 | Ask the lead to start `worker-a` and `worker-b` | Two new panes. `crewtalk who` lists both |
| 3 | Ask the lead to give each worker a task that takes time, as below | Each worker pane shows a message with a `[crewtalk ...]` header. Both work at once |
| 4 | Type half a sentence in the lead pane, and wait | Both replies reach the lead, and the half sentence is still there, unsent |
| 5 | Run `crewtalk log --follow` in another pane | One line per message, for both threads. A held message that is then delivered has two lines with one id |
| 6 | Create `PAUSE`, and make two sends, as below | Each send is refused, and has a `notification.show` line in `~/.config/herdr/herdr-server.log` at its time |

### Step 1: the first prompt

The first prompt comes from the README file itself, so it is word for word. Save
it, and give Claude Code the text of `first-prompt.txt`:

```sh
sed -n '/^   You are the lead\./,/never as my word\.$/s/^   //p' ../../../README.md > first-prompt.txt
```

### Step 3: how to see both workers at once

A short task can end before the other worker starts, so start each task with
`sleep 30`.

1. After both instructions are sent, and before either reply arrives, run:

   ```sh
   crewtalk log
   herdr agent get worker-a
   herdr agent get worker-b
   ```

   When herdr has no name for a worker, `herdr agent get` fails with
   `agent_not_found`. Read that worker's pane by its id:

   ```sh
   herdr pane read PANE_ID
   ```

   The log must hold each worker's instruction and neither reply. Each worker read
   by name must read `working`, and each worker pane must show a running turn.
2. After both replies arrive, run `crewtalk log` again. Both instruction lines must
   come before either reply's line.

### Step 6: two checks for a notice

A notice tells you of a refused send without a look at a pane, and only a real
herdr can show it. Run both checks. A Codex session runs `crewtalk send` outside
its sandbox, so `HERDR_ENV` or the herdr socket may not reach it the way they reach
a Claude Code session.

1. Stop every message:

   ```sh
   mkdir -p -m 700 ~/.crewtalk && touch ~/.crewtalk/PAUSE
   ```

2. Ask the **"lead"** to run this command, and watch the herdr screen:

   ```sh
   crewtalk send --from lead --to worker-a "pause check from the Claude pane"
   ```

   It must exit 3, and the log must get a `refused-paused` line.

3. A lead cannot message a worker during `PAUSE`, so ask `worker-a` in its own
   pane to run this command, and watch the herdr screen:

   ```sh
   crewtalk send --to lead "pause check from the Codex pane"
   ```

   It must exit 3, and the log must get a `refused-paused` line.

4. Check that each refused send has a `notification.show` line at its time:

   ```sh
   grep 'outcome="ok".*method="notification.show"' ~/.config/herdr/herdr-server.log
   ```

5. Check the lead. A new `crewtalk wait --as lead` must exit 3, and a running one
   keeps listening.
6. Allow messages again, and restart the lead's `crewtalk wait`:

   ```sh
   rm ~/.crewtalk/PAUSE
   ```

7. Check the first send after `PAUSE` is gone. Ask the lead to run this command. It
   must exit 0 and print `delivered`:

   ```sh
   crewtalk send --from lead --to worker-a "pause ended check"
   ```

`RESULT.md` records that `notification.show` line, one per pane, with its time. A
refused send with no such line fails this step, because `crewtalk` ignores a failed
notice by design. Whether a person saw the notice on the screen is recorded
separately.
