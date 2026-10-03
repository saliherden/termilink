# Files & artifacts

[← Back to README](../README.md)

## Downloading

`get` copies files from the machine into the chat. It has two modes; the
bot decides which one to use from what you type:

| You type… | Mode | What happens |
| --- | --- | --- |
| `get` | Artifact | Sends all build artifacts of the selected project (newest first) |
| `get debug` | Artifact | Sends only artifacts whose name/relative path contains `debug` (case-insensitive) |
| `get ./release/app.apk` | File | Sends that one file — also `~/…`, `$HOME/…`, absolute or relative-to-cwd paths |

Which mode is used? If the argument looks like a path to an existing
file (`./x`, `~/x`, `…/x`, absolute), it's a File. Anything else (`debug`,
`release/14`, `.md`) is a search keyword against artifact names. So `get
release/14` finds `release/14.apk` even though it contains a slash.

`get` also works as `/get` — both spellings are accepted. Note: if a project
defines a command shortcut named `get`, file fetching wins. Files that still
exceed the sending limit are offered as a temporary link — see
[Size limits](#size-limits).

Which files count as artifacts is per project; they are searched recursively and
newest-first, at most 15 per request:

```yaml
projects:
  app:
    path: ~/code/app
    artifacts: [ "*.apk", "dist/*.zip" ]   # glob vs. file name, or vs. relative path
```

Globs match against the file name (`*.apk`) or, when they contain a `/`, against
the path relative to the project (`dist/*.zip`).

With a project selected, `TERMILINK_PROJECT=<name>` is already exported into the
shell. Add more per-project variables with `env:` — they are exported when the
shell session is (re)created (first command, `/project` switch, or `/exit`):

```yaml
projects:
  app:
    path: ~/code/app
    env:
      APP_PORT: "8080"
      NODE_ENV: production
```

If a command's output exceeds the Telegram message limit, TermiLink does not
truncate silently — a preview is shown and the full text arrives as an
`output.txt` document.

## Uploading

Just send a document in the chat — it is saved into the session's working
directory automatically. Files are never overwritten: a duplicate gets a `-1`
suffix (`notes.md` → `notes-1.md`). A document over the sending limit is
rejected with a clear message.

## Size limits

Telegram lets bots send single documents up to 50 MB. TermiLink handles
bigger files like this, using [`telegram.max_file_bytes` and
`telegram.big_file_link_host`](configuration.md#telegram):

- ≤ 50 MB → sent directly as a document.
- > 50 MB → zipped on the fly first; if the archive fits, it is sent as
  one document (nice for logs, dumps, text).
- Still > 50 MB → needs `big_file_link_host` to be set. The file is then
  queued behind an owner approval in chat — the bot asks "File exceeds
  Telegram's 50MB sending limit. Send it via a temporary link on `<host>`?"
  Confirming with `yes` / `evet` / `ok` uploads a `.tar` archive of the
  file (so restricted types like `.apk` pass) and posts the download link as a
  message (tap and save on your phone).
- `big_file_link_host` blank → the clear size error is kept, nothing
  leaves the machine.

## External links

⚠️ External links are anonymous public URLs hosted outside your machine.
Do not use this path for secrets, credentials or customer data.

| host | size cap | retention | deleteable by TermiLink? |
| --- | --- | --- | --- |
| `uguu.se` | ~128 MB | auto-deleted after ~3 hours | yes, by the host's own timer |
| `catbox.moe` | 200 MB | persists — removed only if the host purges it for inactivity | no |

The URL is unguessable, but the file is served to anyone who has it and the host
operator can read it for as long as it is up — with `catbox.moe` that includes a
`.tar` of what you sent, potentially indefinitely, on infrastructure you do not
control. The host is a single global setting in `config.yaml`, so every oversized
file goes through it; leave it empty and accept the size error unless you have a
reason to send something off the machine.

Owner approval gates the transfer and every step is audited (`approval_*`,
`file_link`), but approval is a prompt in the chat, not a confidentiality
control: approving it is the act of publishing the file.

## Access control

Owner: unrestricted. Workers: must select a project; every delivery is vetted
against the workspace policy. See [Security](security.md).
