# Security

## Report a problem

Report a security problem privately, with GitHub's private vulnerability reporting: on this
repository's Security tab, choose "Report a vulnerability". Please do not open a public issue for it.

## What CrewTalk does not defend against

CrewTalk carries messages between agent sessions that run as one user on one machine. It has no
accounts and no encryption. The README's [Good to know](README.md#good-to-know) and
[Pause everything](README.md#pause-everything) sections give the limits that follow: a sender's name
is a claim, one agent can make another send, message text is not secret, and the stops are guard
rails.

Please report anything that lets CrewTalk reach past these limits, such as a send that reaches
another user's session.

## What reaches your terminal

1. `crewtalk who`, `log` and `wait`, and error lines, show control bytes and invalid UTF-8 as visible
   escapes such as `\x1b`. They keep newline and tab. So text that holds the four characters `\x1b`
   looks the same as an escaped byte.
2. `--json` output stays valid JSON. It writes control characters as `\u` escapes, so decoding gives
   the original text. Invalid UTF-8 decodes as U+FFFD.
3. Names and threads refuse control characters and invisible format characters. They allow U+200C and
   U+200D, which real names use, and those two can make two names look the same.
4. Message text can still hold format characters such as U+202E, which can reorder a line on some
   terminals. Message text can also hold a line that looks like a header. Only the first line of a
   message is its real header.
