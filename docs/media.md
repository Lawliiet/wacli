# media

Read when: downloading media from a synced message.

`wacli media` downloads media referenced by messages already stored in `wacli.db`.

## Commands

```bash
wacli media download --chat JID --id MSG_ID [--output PATH]
wacli media backfill [--chat JID] [--limit N] [--workers N]
wacli media retry [--chat JID] [--type TYPE] [--before YYYY-MM-DD] [--limit N] [--batch N] [--wait DUR]
wacli media rules check PATH
wacli media rules test PATH --chat JID --type TYPE [--size N] [--ext EXT] [--mime MIME] [--in-list LIST]...
```

## download

Downloads media for a single message.

### Notes

- The target message must already be synced.
- Media downloads are capped at 100 MiB.
- `--output` may be a file path or directory.
- If `--output` is omitted, media is written under the store media directory.
- `--read-only` is supported only with explicit `--output`; it writes the file without opening the WhatsApp session store or recording `local_path` / `downloaded_at`.
- Because it never opens the store for writing, `--read-only` also takes **no store lock**, so it is the way to fetch media while `sync --follow` is running. A follow session holds the lock for its entire run, so a plain `media download` cannot proceed until it stops, and `--lock-wait` only turns the immediate failure into a timeout.

### Examples

```bash
wacli media download --chat 1234567890@s.whatsapp.net --id ABC123
wacli media download --chat 1234567890@s.whatsapp.net --id ABC123 --output ./downloads
wacli media download --chat 1234567890@s.whatsapp.net --id ABC123 --output ./photo.jpg
wacli --read-only media download --chat 1234567890@s.whatsapp.net --id ABC123 --output /tmp/photo.jpg
```

## backfill

Downloads media for every already-synced message that has downloadable metadata
but no local copy yet, over a single connection.

`sync --download-media` only downloads media for messages that *arrive during*
the sync session. Media for messages synced earlier is never fetched by sync;
`media backfill` closes that gap by scanning existing rows.

### Notes

- Files are written under the store media directory (same layout as `download`).
- `--chat` scopes the backfill to a single chat JID.
- `--limit` caps how many files to download (0 = all); newest messages first.
- `--workers` sets the number of concurrent downloads (default 4).
- Runs until completion or interruption by default; explicitly set global `--timeout` to cap a run.
- Requires a writable store; not available in `--read-only` mode.
- Reports counts: pending (total matching), attempted, downloaded, skipped, failed.

### Examples

```bash
wacli media backfill                                   # download all pending media
wacli media backfill --limit 50                        # download the 50 newest pending
wacli media backfill --chat 1234567890@s.whatsapp.net  # one chat only
wacli media backfill --json                            # machine-readable counts
```

## retry

Recovers media that expired off WhatsApp's CDN. `media download` and `media
backfill` fetch directly from WhatsApp's servers, which only keep media for a
limited time — older media returns HTTP 403. `media retry` instead asks the
primary device (your phone) to re-upload the media via WhatsApp's media-retry
protocol (the same mechanism WhatsApp Web uses), then downloads it.

Recovery only works while the phone is online and still holds the media. Media
the phone no longer has is marked unavailable only after the stored CDN path is
also confirmed expired, so later runs can skip genuinely unavailable rows.

### Notes

- Requires a writable store and an online phone; not available in `--read-only` mode.
- Run `media backfill` first; retry is intended for media whose direct CDN download failed.
- Successful retry responses download the re-upload without reusing the original ciphertext hash. The original media-key HMAC, plaintext SHA-256, size limit, and declared file length still apply; failed verification leaves the media pending and does not replace a local file.
- Retry receipts are sent in batches (`--batch`, default 32) with a second
  attempt for non-responders; `--wait` (default 30s) bounds each attempt.
- `--chat` scopes to one chat; `--before YYYY-MM-DD` scopes to media older than a date.
- `--type` scopes to one kind of media: `image`, `video`, `gif`, `audio`,
  `document` or `sticker`. Voice notes are stored as `audio`, so
  `--type audio` recovers them without also re-uploading every expired photo
  and video, which usually make up most of the bytes. Any other value is
  rejected.
- `--limit` caps how many messages to retry (0 = all pending); newest first.
  It counts only the messages that match the other filters.
- Runs until completion or interruption by default; explicitly set global `--timeout` to cap a run.
- Reports counts: requested, recovered, not_on_phone (gone), no_response, failed.
- `no_response` means the phone did not answer in time (often transient) — those
  stay pending and can be retried later; only `not_on_phone` is marked gone.

### Examples

```bash
wacli media retry                                    # try to recover all pending media
wacli media retry --chat 1234567890@s.whatsapp.net   # one chat only
wacli media retry --before 2026-01-01                # only media older than a date
wacli media retry --type audio                       # only audio, voice notes included
wacli media retry --limit 50 --wait 45s --json       # bounded run, machine-readable
```

## Automatic download rules

Senders sometimes delete a message, and once it is deleted WhatsApp's servers
usually refuse the media download. `wacli sync --download-rules PATH` fetches the
media you choose the moment it arrives, so a later deletion cannot take it away.
`--download-media` (everything, no filter) keeps working unchanged; the two flags
are mutually exclusive.

The rules file is JSON and is re-read whenever its mtime or size changes, so
another program can edit it while `sync --follow` runs. An invalid edit keeps the
last good rules and logs a `download_rules_invalid` warning; a missing file means
nothing is downloaded automatically. The file is validated strictly: unknown keys
are errors.

```json
{
  "version": 1,
  "rules": [
    {"name": "dms", "match": {"kind": "dm"}, "types": ["image", "video", "audio", "document"], "extensions": ["pdf", "zip"], "max_bytes": 26214400},
    {"name": "family", "match": {"list": "Family"}, "types": ["image", "video"], "max_bytes": 52428800},
    {"name": "one group", "match": {"chat": "1203...@g.us"}, "types": ["document"], "max_bytes": 10485760},
    {"name": "no groups", "match": {"kind": "group"}, "download": false}
  ],
  "limits": {"total_bytes": 5368709120, "keep_days": 90}
}
```

- `match` keys are ANDed; an empty `match` is any chat. Each key takes one string or a list (any of).
  - `chat`: chat JIDs.
  - `kind`: `dm`, `group`, `channel`, `status`.
  - `list`: a chat list id or name, resolved through the local chat lists (`wacli lists`) when the message arrives. An id survives renames; an unknown, deleted, or ambiguous name never matches.
- Filters, also ANDed with `match`: `types` (`image`, `video`, `gif`, `audio`, `sticker`, `document`; voice notes are recorded as `audio`), `extensions` (from the file name, or from the MIME type when there is no name), `mime` (globs such as `application/pdf` or `video/*`), `max_bytes` (the message's declared file length; an unknown size passes).
- `"download": false` makes a matching message an exclusion.
- The first rule whose match and filters all pass wins; order matters. A message that fails a rule's filters falls through to the next rule. No match means no download.
- Status broadcasts are never downloaded unless a rule names them with `kind: "status"` or their JID in `chat`; an empty `match` does not include them.
- Only live messages are considered. Messages that arrive through history sync are not downloaded by the rules (use `media backfill`).

### Limits

`limits.total_bytes` and `limits.keep_days` bound the files the rules downloaded.
When either is exceeded, the oldest automatically downloaded files are deleted
(on start, after each automatic download, and hourly). Only the files go: the
message rows, text, and media metadata stay, and `local_path`/`downloaded_at` are
cleared so `media download` or `media backfill` can fetch the file again if the
server still has it. Eviction never touches:

- files downloaded by hand (`media download`, `media backfill`, or `sync --download-media`); the store records which downloads were automatic, and a manual download of a message removes it from that record;
- files of messages the sender has since deleted. Those still count toward `total_bytes`, so a store full of deleted-message media can sit above the cap.

### rules check / rules test

Both work offline, without opening the store.

- `media rules check PATH [--json]` validates the file and prints the parsed, normalized rules.
- `media rules test PATH --chat JID --type TYPE [--size N] [--ext EXT] [--mime MIME] [--kind KIND] [--in-list LIST]... [--json]` says which rule an imagined message matches. The chat kind comes from the JID. List membership is not looked up; pass `--in-list` (same id or name as in the rule) to pretend the chat is in a list.

### Events and debugging

With `sync --events`, each decision is a `media_auto_decision` event, each finished
download a `media_auto_downloaded` event with a running total, and each eviction
pass a `media_auto_evicted` event. Without `--events`, set `WACLI_DEBUG=1` to print
decisions to stderr.
