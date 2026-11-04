# Contributing

This file is for people who change crewtalk. It holds the setup, the checks, the journeys and the
test inputs. It also holds the full behavior details behind the short `README.md`.

## Contributors

You need these besides what `README.md` lists:

- first-commit, set up as below.
- shellcheck and staticcheck on your PATH, because `./scripts/check fast` runs them. This project
  is checked with shellcheck 0.11.0 and staticcheck 2026.2.2. Get them from
  `https://github.com/koalaman/shellcheck/releases` and
  `https://github.com/dominikh/go-tools/releases`.

Commits and pushes run pre-commit hooks.
They run the `first-commit` command on your PATH.

Install first-commit and pre-commit by following the first-commit README, at
`https://github.com/amrendra18/first-commit`. Both commands must be on your PATH, so add their
folders if the install does not. Then, in each clone, run:

```sh
first-commit setup
```

It sets this clone's Git identity and SSH signing, installs the Gitleaks
scanner and the Git hooks, and does not change files this project owns. The first-commit README
lists what else its install and setup change on your machine.
In a terminal it asks for your public identity once per machine.

Check this clone with `pre-commit run first-commit-check --hook-stage manual --all-files`.

## Routine use

Run the quick checks before each commit:

```sh
./scripts/check fast
```

It runs these steps in order:

1. `gofmt` checks Go formatting.
2. Every test that `AGENTS.md` names must exist. When `AGENTS.md` names packages after a test, as
   in "`TestLog*` in `cmd/crewtalk` and `internal/journal`", the test must exist in each of them. A
   journey run by hand must have its heading in `verify/hand-journey.md`.
3. `verify/features/` must hold one file per `scripts/verify` journey. Each file names the
   journey's use case and when it counts as proven.
4. `shellcheck` checks every shell script.
5. `go vet` and `staticcheck` check every package, each on the normal build and on the
   `-tags crewtalk_test` build.
6. `go test` runs every package's tests.

Run the full checks before each pull request:

```sh
./scripts/check full
```

It runs the quick checks, then one `scripts/verify fast` run, which must prove every
journey except `codex-rule`, which it must report as skipped:

1. In `claude-claude`, two `crewtalk wait` processes stand in for Claude sessions and exchange
   one message each way. No Claude session takes part.
2. In `by-name`, the same two listeners send by their joined names, and the answer uses
   `--reply-to`. The reply must carry the first message's thread, turn 2 and its id.
3. In `held`, a send to a joined Claude name with no listener is held, and the next
   `crewtalk wait` for that name prints it.
4. In `limits`, the seventh turn of a thread is refused with exit 3, and so is a send while
   `PAUSE` exists.
5. In `two-projects`, a `crewtalk wait` listener stands in for the **"lead"**. It sends one
   instruction by joined name to each of two Codex threads, and restarts after each message. Each
   reply must use `--reply-to` and keep its own thread at turn 2.
6. In `claude-codex` and `codex-claude`, a `crewtalk wait` listener and a Codex session make one
   round trip.

The first four journeys need no settings. Each uses its own temporary `CREWTALK_HOME`. So
`scripts/verify fast` run alone with no thread settings proves those 4, skips `two-projects`,
`claude-codex`, `codex-claude` and `codex-rule`, and exits 0.

`two-projects` and the round trip use a test input: the fake codex
`internal/route/codex/testdata/codex` runs the reply command in place of `codex`, with a temporary
`CREWTALK_HOME` under `/tmp` and two fixed fake thread ids.
For this run only, `scripts/verify` builds `crewtalk` with `-tags crewtalk_test`, the only
build that reads `CREWTALK_CODEX_BIN`.
A `codex queue` call must take under 1.9 seconds, measured by `claude-codex`. Each delivery on the
Claude route must take under 0.1 seconds, measured by `claude-claude`, `by-name`, `limits` and
`codex-claude`. `held` and `two-projects` measure no bar.
Then more test inputs must make journeys fail or be refused:

1. The same fake in its unknown-thread mode must make `claude-codex` fail.
2. A stand-in that waits 2 seconds before it runs the fake must make `claude-codex` miss the
   `codex queue` bar.
3. `CREWTALK_VERIFY_CLAUDE_BAR=0` sets the Claude route bar to 0 seconds, so `claude-claude`
   and `codex-claude` must miss it. `scripts/verify` accepts this setting only together with
   `CREWTALK_CODEX_BIN`.
4. A fake worker whose reply never arrives must make `two-projects` fail.
5. A stand-in worker that replies without `--reply-to` must make `two-projects` fail.
6. `scripts/verify full` without the second thread must skip `two-projects` and exit 1.
7. The same thread given twice must exit 64.
8. A stand-in `codex` that answers with no decision, then one that answers `allow`, must make
   `codex-rule` fail.
9. A stand-in `codex` that allows `PATH=/x crewtalk send`, then one that allows
   `CREWTALK_HOME=/x crewtalk send`, must make `codex-rule` fail.

One more test input must work: a stand-in `codex` that allows only `crewtalk send` must get
`codex-rule` proven.

A `codex`, a `claude` and a `herdr` command that refuse to run come first on PATH, so no real
Codex, Claude or herdr session is contacted. This check therefore does not prove delivery to real
sessions.
Both `./scripts/check` modes and `scripts/verify` unset `HERDR_ENV`, so no run shows you a
notice.
Each `./scripts/check full` run adds twelve evidence folders under `.local/verify/`.
Each folder's `summary.json` names the commit it built (`commit`), whether a file differed
from that commit (`dirty`), and the sha256 of the `crewtalk` it built (`binary_sha256`).
`dirty` is true for a tracked change and for an untracked file that `.gitignore` does not
ignore.

The live check is `scripts/verify full`. Two Codex sessions must be open. Each gets a request and
runs a reply command. Each session's `CODEX_THREAD_ID` holds its thread id. From the root of your
clone, with those two different ids in place of `THREAD_UUID_1` and `THREAD_UUID_2`, run:

```sh
CREWTALK_VERIFY_CODEX_THREAD=THREAD_UUID_1 CREWTALK_VERIFY_CODEX_THREAD_2=THREAD_UUID_2 \
  scripts/verify full
```

It runs every journey, and a journey skipped for a missing setting makes it exit 1. It refuses
`CREWTALK_CODEX_BIN` with exit 64, because it needs the real `codex`. The `crewtalk` on your PATH
must be the same build as the one under test, so run the install command in `README.md` first.
Otherwise `scripts/verify` sends nothing, exits 3 and names the fix.

Its `codex-rule` step runs the real `codex execpolicy check` against
`~/.codex/rules/default.rules`. `crewtalk send` must get the decision `allow`. `herdr agent
prompt`, `PATH=/x crewtalk send` and `CREWTALK_HOME=/x crewtalk send` must not.
`scripts/verify fast` reports this step as skipped and does not fail.

The `claude-codex` and `codex-claude` journeys of `scripts/verify full` prove that a real Codex
session gets the request and runs the reply. Every journey still listens with `crewtalk wait` in
place of a Claude session, and `claude-claude` runs no session at all. So no script proves that
the end of `crewtalk wait` wakes a real Claude Code session. Only a journey by hand with real
Claude Code sessions proves that. `verify/hand-journey.md` holds the steps of the checks no
script runs.

When a `scripts/verify` run ends, it stops every `crewtalk wait` it started, and runs
`crewtalk leave` for each name it joined, so no `verify-` name stays in `crewtalk who`.
`./scripts/check full` fails when a `crewtalk wait` from any of its runs still runs.

Both commands end with the line `check fast: exit <code>` or `check full: exit <code>`.
The exit codes are:

- 0: every check passed.
- 1: a check failed. The line before names it.
- 64: the mode is missing or unknown, or extra arguments were given. The last line is then
  `check usage: exit 64`. `./scripts/check --help` prints the usage.

## Behavior details

### Status

crewtalk works today. It has been run on one Mac, on macOS, with real Claude Code and Codex
sessions, and on no other machine. That Mac's agents also had crewtalk's rules in their global
instructions, so the README alone has no run with real sessions yet.

Each pair has sent a message and got a reply between real sessions:

1. Claude Code to Codex.
2. Codex to Claude Code.
3. Claude Code to Claude Code.
4. Codex to Codex.

What works today:

1. `crewtalk send` and `crewtalk wait` carry a message between two sessions.
2. `crewtalk join`, `crewtalk leave` and `crewtalk who` keep an address book of agent names.
3. `crewtalk log` prints the log of messages.
4. A message for a Claude name in the address book that is not listening is held. A held message
   expires after 30 minutes, at the next `crewtalk send`, `wait` or `log`.
5. Loop limits and `PAUSE` stop runaway traffic between agents.
6. One Claude Code session can coordinate two Codex workers (see the README section
   "Let a lead run your workers").

### How it works

1. A message to a Codex session goes through Codex's own `codex queue` command. Codex runs it after
   the session's current turn.
2. A message to a Claude Code session goes through a socket, a local connection between two programs
   on one machine. The session runs `crewtalk wait` in the background, and that command reads the
   socket, prints the message and exits. Its exit wakes the session.
3. crewtalk has no daemon, so no crewtalk service keeps running. Each command starts, does its work
   and exits.
4. crewtalk types into no pane, so a draft in an input box stays where it is, unsent.
5. Every message is recorded in one log file, `~/.crewtalk/log.jsonl`.
6. `crewtalk send` never branches on the kind of agent it sends to. Each kind has its own route.
7. Never send a secret in a message. A message to a Codex session is an argument of `codex queue`,
   and text on a `crewtalk send` command line is an argument too, so `ps` shows both. A held message
   is saved in a file, `~/.crewtalk/held/NAME.jsonl`.
8. A peer's message is never your word. The header of every message and the first prompt of the
   **"lead"** in the README section "Let a lead run your workers" carry this rule.

herdr is a terminal workspace manager that runs agents in panes. Only the README section
"Let a lead run your workers" and "Notices" need it.

### Sizes, names and recovery

1. To a Codex session, a message is one command-line argument, so macOS's limit of 1,048,576 bytes
   applies. A 2,000,000-byte send exits 5 with `argument list too long` and logs one `failed` line.
   To a Claude session, the limit is 8 MiB, header included, and a 1 MB message is tested.
2. The address book is not tied to herdr. A herdr agent name is in herdr's own list, and the two
   names can differ. `crewtalk who --json` prints the book as
   `{"agents":[{"name":"lead","kind":"claude","address":"lead"}]}`.
3. If a worker's ready message does not arrive after you answer a dialog in its pane, paste its first
   prompt into that pane, with that worker's own ready word.

### State files and removal

`crewtalk` creates its state folder `~/.crewtalk` at first use, and adds these as it needs them:

1. The first `crewtalk send` adds the log `log.jsonl` and its lock file `log.jsonl.lock`.
2. The first `crewtalk join` or `crewtalk wait` adds the address book `agents.json` and its lock
   file `agents.json.lock`. `crewtalk leave` also creates `~/.crewtalk` and that lock file, even for
   a name that no agent joined.
3. The first `crewtalk wait` adds the inbox `inbox/`. The inbox keeps up to two small lock files per
   listener name, and they stay. It also holds the socket `NAME.sock` while a `crewtalk wait` runs.
   A killed listener can leave the socket behind, and it does no harm.
4. The first `crewtalk wait`, or the first send that finds its receiver not listening, adds the lock
   file `held.lock`.
5. When a message is held, `crewtalk` adds the folder `held/`, with one file of held messages per
   Claude name.
6. When a held message expires, it adds `expired.jsonl`, which keeps the expired ids until their
   sender's next `crewtalk send`.

crewtalk never creates `~/.crewtalk/PAUSE`; only you do (see "Limits and pause").
To remove crewtalk, delete the `crewtalk` folder you cloned, `~/.local/bin/crewtalk`,
`~/.crewtalk`, and the `crewtalk send`
line in `~/.codex/rules/default.rules` that you added. The empty rules file and its
folder may stay. An empty `~/.local/bin` can also stay, and so can `~/.local`. Also delete the PATH
line if you added it only for crewtalk, and the text from the README's "Tell your agents about
CrewTalk" if you added it to global instructions. Go's caches and counters stay unless you remove
them.

### Messages between sessions

The examples here that leave out `--from` run in a Codex session. In a plain terminal they exit 64.

A Claude session listens in the background, and gets its name in the address book. To start this,
tell Claude Code to run this command in the background. The first prompt in the README section
"Let a lead run your workers", "What you do", step 1, is the full wording to use,
and it names no herdr command:

```sh
crewtalk wait --as lead
```

`crewtalk wait` exits after one message, and that exit wakes the session. The session starts
`crewtalk wait --as lead` again to receive the next message. `crewtalk wait` waits with no time
limit by default. With `--timeout`, as in `crewtalk wait --as lead --timeout 30m`, it stops waiting
after that time and exits 4.

A message that arrives while no `crewtalk wait --as lead` runs is held, because `lead` is in the
address book: `crewtalk wait --as lead` and `crewtalk join` add it. `crewtalk send` then
exits 0 and prints `held`, and the log gets a `held` line. The next `crewtalk wait --as lead`
prints the oldest held message at once and exits, unless `PAUSE` exists (see "Limits and
pause"); the log gets a `delivered` line with
`held_for`, the seconds the message was held. A send to a Claude address that is not in the
address book is not held. It exits 4, so a mistyped name is caught at once.

A held message expires at the first `crewtalk send`, `wait` or `log` that runs 30 minutes or
more after it was held. If no such command runs, it stays held, and nobody is told. When it
expires, it is removed, and the log gets an `expired` line. The sender sees one stderr line
naming the expired ids at its next `crewtalk send` that is not refused, and you get a notice
(see "Notices").

The Claude side adds a Codex worker to the address book under a name:

```sh
crewtalk join --as worker-a --kind codex --address THREAD_UUID
```

`THREAD_UUID` is the worker's thread id. The worker's `CODEX_THREAD_ID` holds it, and it is
also shown as the sender, `codex:THREAD_UUID`, of every message the worker sends. The `thread ...`
in a message header is a different value, the conversation.

Either side sends by name, and a reply keeps the conversation. A Codex session runs only
`crewtalk send`, the one command its rule allows outside the sandbox. `crewtalk send` reads
the address book, so names work there too:

```sh
crewtalk send --from lead --to worker-a "Please re-check the plan"
crewtalk send --to lead --reply-to ID "Done: no blocking issue"
```

A Claude session names itself with `--from`. The value must be a joined name, or KIND:ADDRESS such
as `claude:anyone`. `crewtalk wait --as lead` joins the name `lead`. A Codex session is known by its
thread, so it leaves `--from` out. `ID` is the id in the header of the message being answered.
`crewtalk who` lists the address book, and `crewtalk leave --as worker-a` removes a name from it.
`crewtalk log --follow` first prints the existing log, then each new message as it is sent, with its
id. It runs until you stop it with Control-C. Each message starts with a header line naming its
sender, so the receiver can tell it from its own operator.

Text that starts with `-` needs `--` before it, or goes on standard input with `-`:

```sh
crewtalk send --from lead --to worker-a -- "- first item"
printf '%s\n' "- first item" | crewtalk send --from lead --to worker-a -
```

A Codex session has more limits on what it can send (see "What a Codex worker can send").

Give Claude names that differ by more than case. On a case-insensitive volume, the macOS
default, `lead` and `LEAD` share one listener and one held file, so a message for one can reach
the other. A `crewtalk wait --as LEAD` that exits 5 still adds the name `LEAD`, so remove it with
`crewtalk leave --as LEAD`.

What a `crewtalk send` exit code tells the sender:

- 0: delivered, or held for a Claude name in the address book that is not listening.
- 3: refused by a limit or by `PAUSE`; nothing was delivered. The stderr line says why and
  what happens next.
- 4: not delivered, because no agent joined the name, or the Codex thread is unknown or not
  running. A Claude address that is not in the address book also gets exit 4 when nobody
  listens. So does a joined Claude name when the message could not be held. When the hold step
  finds a listener, the send tries once more, so a send to a busy listener can take 20 s or more
  before exit 4, and longer while another crewtalk command holds a lock. Exit 4 then means that
  this try did not reach the listener either. Nothing was delivered, so it is safe to send again
  once the receiver exists.
- 5: the route failed, or the outcome is unknown, so the message may have arrived. Check
  `crewtalk log` before you send again; the log may have no line for it, and crewtalk never
  retries on its own. A Codex worker that gets exit 5 tells you in its pane, or in its next send,
  and the session that coordinates it checks `crewtalk log`. A warning
  line, starting `crewtalk send: warning:`, can come before the stderr line.
  A log that cannot be opened before the send also gets exit 5. Its stderr line then ends
  `nothing was sent`. When the stderr line says the log is busy, send again in a moment. When it
  says the log is not writable, it names the `chmod` for a file mode problem. That `chmod` fails
  when a folder's mode is the cause, so then run `chmod 700 ~/.crewtalk`. In a Codex session, a log
  that is not writable can also mean the shell line missed the Codex command rule. Then send again
  in a form from "What a Codex worker can send", and do not ask to leave the sandbox.
- 64: a usage error, and nothing was sent. `--from` gets this when it is a bare name that no agent
  joined, or when it is left out outside a Codex session. Use a joined name, or KIND:ADDRESS such as
  `claude:anyone`. `--reply-to` gets this when its id is not in the log
  yet, because a receiver can reply before the sender's log line is written, or because the id is
  wrong. Check the id, and send once more in a moment. If it still fails, tell your operator.
  crewtalk does not retry this on its own.

If the log line cannot be written after a delivery, `crewtalk send` still exits 0, prints one
stderr line, and its `--json` output gains `"logged": false`.

The log result of each message is one of `delivered`, `held`, `expired`, `not-listening`,
`receiver-unknown`, `outcome-unknown`, `failed`, `refused-limit` or `refused-paused`.
A log line that is not JSON is skipped with a warning naming its line number. To stop the
warning, remove that line from `~/.crewtalk/log.jsonl`. Do not rewrite it by hand: the limits
count only lines in the form crewtalk writes.
A line in a held file (`~/.crewtalk/held/NAME.jsonl`) or in `~/.crewtalk/expired.jsonl` that
is not JSON is also skipped with a warning naming the file and the line number.
`crewtalk send` and `crewtalk wait` go on with the other lines. crewtalk keeps that line and
moves it to the end of the file when it rewrites the file, so the line number can change. A
command that rewrites the file can warn twice about one line, with different line numbers. To
stop the warning, remove that line.

### What a Codex worker can send

The Codex command rule matches a plain shell line, so the text goes in one quoted string with no
backslash.

1. Single quotes keep backticks, dollar signs and double quotes literally, also over several lines.
   An illustration:

   ```sh
   crewtalk send --to lead 'Ran `go test`, it cost $5, and it printed "ok".
   Second line.'
   ```

2. Double quotes hold an apostrophe, also over several lines. An illustration:

   ```sh
   crewtalk send --to lead "It's done.
   It's tested."
   ```

3. Text with an apostrophe and also a double quote, backtick or dollar sign fits neither form, so
   reword it. For example, write `It is` in place of `It's`.
4. Text that starts with `-` goes after `--`, as in `crewtalk send --to lead -- "- first item"`.

A form with a backslash escape in the text runs in the sandbox, gets exit 5, and sends nothing. That
includes `'it'\''s'` and `"\$5"`. A pipe may not match the Codex command rule, so a worker does not
use `-` for standard input.

These results come from Codex with a read-only sandbox, on-request approvals and its automatic
approval review.

A worker must not ask to leave the sandbox. Codex's approval review can refuse a message based on
a peer's instruction, and then the worker can stall. The exit 5 line tells the worker to send again
in a form above, and to tell you if that also fails.

The Codex command rule lets a form above run with no approval review, even when a peer asked for the
message. So one agent can make another send a message without Codex reviewing it, and the receiver
may take that text as a task. Outside the sandbox, `crewtalk send` reads and writes `~/.crewtalk`,
runs `codex queue`, and runs `herdr notification show` inside herdr. crewtalk checks only that
`--from` is a joined name or has the form KIND:ADDRESS, such as `claude:anyone`, so the sender name
in a header is a claim, not proof.

### Limits and pause

crewtalk stops a loop between agents. It refuses a send, with exit 3, when:

1. The send's turn is 7 or more. The turn comes from `--reply-to`, which takes the answered
   message's turn plus 1, or from `--turn`, which is 1 when it is left out. So this limit
   catches a conversation whose replies use `--reply-to`. The stderr line names the turn and
   the thread, and tells the sending agent to stop this conversation and report to you. A send
   without `--reply-to` and with a low `--turn` is not refused by this limit, even in the
   same thread. You decide whether the agents start a new conversation.
2. The same sender already sent 20 messages to the same receiver in the last hour, counted
   from the log. This limit is the backstop for a loop between one sender and one
   receiver, whatever turn its sends give. A held message counts once, from the time it was
   held. The stderr line names the time when the next send is allowed: when fewer than 20 of
   them are left in the last hour.
3. The file `~/.crewtalk/PAUSE` exists. The stderr line tells the sending agent that every
   send is refused while it exists and to report to you. A new `crewtalk wait` also stops: it
   prints one stderr line that tells the agent to report to you, takes no message, and exits 3.
   Only you remove `PAUSE`, with the `rm` command below.

These limits stop a runaway loop, which sends one message at a time. They are not a hard
quota: sends from one sender to one receiver at the very same moment can pass 20.

To stop every message:

```sh
mkdir -p -m 700 ~/.crewtalk && touch ~/.crewtalk/PAUSE
```

To allow messages again:

```sh
rm ~/.crewtalk/PAUSE
```

A refused send delivers nothing. It writes one log line, `refused-limit` or `refused-paused`,
prints one stderr line, and gives you a notice. Warnings about a broken log or held file, each
starting `crewtalk send: warning:`, may come before that line.

While `PAUSE` exists, a held message stays held and no agent gets it. A `crewtalk wait` that was
already listening keeps listening, and no send reaches it. A held message can still expire
during `PAUSE`, at the first `crewtalk send`, `wait` or `log` that runs 30 minutes or more after
it was held. It is then never delivered, and you get a notice. A `crewtalk wait` refused during
`PAUSE` adds no name to the address book.

One rare case still prints a message during `PAUSE`: a `crewtalk wait` that was already listening
reopens its socket after a stalled connection, and it can then print one held message. The
session's replies are still refused.

A session whose `crewtalk wait` exits 3 reports to you and does not start it again until you
say so. After you remove `PAUSE`, restart `crewtalk wait --as NAME` in each session. Restarting
is harmless: if a listener still runs, the second one exits 5 with "NAME already has a listener",
and the first keeps running. The new `crewtalk wait` prints the oldest held message first.

### Notices

crewtalk tells you about a refused send and an expired message with a herdr notification. It
does so only inside herdr, when the `herdr` command is on PATH and `HERDR_ENV=1`. Elsewhere it
shows nothing. A notice never makes a command fail.

herdr records each notice it is sent in `~/.config/herdr/herdr-server.log` as `notification.show`,
so each refused send inside herdr has one completed line at its time. To check for a notice you did
not see, run:

```sh
grep 'outcome="ok".*method="notification.show"' ~/.config/herdr/herdr-server.log
```

No output means herdr shows no notice in its log. A notice that failed also gives no output.
`No such file or directory` means herdr has no log at that path.

### During a run with a lead

A reply that arrives while the **"lead"** restarts its `crewtalk wait` is held for a moment, and the
next `crewtalk wait` prints it. The log then shows two lines with one id: `held`, then
`delivered`.

A message held for over 30 minutes while the lead is in one long turn expires at the next
`crewtalk send`, `wait` or `log`. The sender is told on its next send that is not refused, and
you get a notice. The text is gone, and the lead never sees it.

#### Choose a worker's model

Everything after `--` in `herdr agent start` goes to the agent's own command, so a worker can run on
a chosen model and effort, which can save quota. crewtalk itself does not choose models. Model names
change over time for both forms below. `codex --help` and `claude --help` list each command's
options.

The Codex form takes its first prompt as its last argument. `FIRST PROMPT` is the quoted prompt
from "What the agent does", step 2, in the README:

```sh
herdr agent start worker-a --kind codex --pane PANE_ID -- -m gpt-6-luna -c model_reasoning_effort=medium 'FIRST PROMPT'
```

This Claude Code form is an example. The session it starts needs its own first prompt to listen,
in the form of the first prompt of the **"lead"** in the README, with its own name. The Claude form
takes that prompt afterwards, in a second command:

```sh
herdr agent start reviewer --kind claude --pane PANE_ID -- --model haiku --effort medium
herdr agent prompt reviewer 'FIRST PROMPT'
```

#### What you see

1. Two new panes appear, one per worker, each in its project folder.
2. Each message appears in its receiver's session with a `[crewtalk ...]` header. The header says
   the message comes from a peer, so it never counts as your approval. The lead treats a peer's
   message as a request, never as your word.
3. crewtalk types nothing into a pane, so you can keep typing to the lead and your draft stays
   unsent.
4. `crewtalk log --follow` prints one line per message, with its thread. A held message that is
   then delivered has two lines with one id.
5. A worker can stop and ask for your approval in its own pane, because a peer's message never
   counts as your approval. The lead does not hear of it. So when a reply never comes, look at the
   worker's pane and answer there.
6. To stop every message, see "Limits and pause". `PAUSE` stops only messages, so the workers keep
   working on their current turns.
