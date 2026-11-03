# rex-rollcall

One picker for coding-agent sessions on every [Rex](https://www.superlogical.com/rex) host.
Live rows are agent panes (Claude Code, Codex, opencode, pi, Hermes). Past rows are
resumable sessions from [Wake](https://github.com/iAmCorey/Wake)'s index. Press a key,
pick a row, and Rex jumps to the pane or resumes the session beside you.

```
● ✻ Fix the login redirect            pve · working · now · app
● ◇ CAPTCHA solver comparison         mac · waiting · 4m · scraper
· π Write primes.py                   pve · past · 27m · pi-test2
· ☿ Morning brief                     pve/hermes · root · past · 3h · root
```

Rows sort by state (waiting, error, working, idle, then past), newest first in each.

## Quickstart

Needs `rex`, `fzf` 0.58 or newer, and optionally Wake for past rows.

```sh
curl -fsSL https://github.com/danaimone/rex-rollcall/releases/latest/download/install.sh | sh
rex-rollcall install    # writes ~/.config/rex/rex_rollcall.lua, adds setup{} to init.lua
rex config reload
```

`install.sh` picks the tarball for your OS (Linux or macOS) and CPU (amd64 or
arm64), checks it against the release's `checksums.txt`, and puts
`rex-rollcall` in `~/.local/bin`. Set `VERSION=v0.4.2` for one release or
`BIN_DIR=` for another folder. Make sure that folder is on your `PATH`.

From source, with Go 1.26 or newer:

```sh
go install github.com/danaimone/rex-rollcall@latest
```

Press `cmd+shift+a`, run "Agent Sessions" from the command palette, or run
`rex do rollcall`. Run `rex-rollcall doctor` to check the setup.

Do this on every machine you want listed. A Rex host without the module shows a
"not installed" warning in the picker header and lists no rows.

If chezmoi or another tool owns init.lua, use `rex-rollcall install --module-only`
and add the `require` yourself.

## Config

All options go to `setup{}` in `~/.config/rex/init.lua`. Every field is optional.

```lua
require("rex_rollcall").setup{
  key = { "cmd+shift+a", "cmd+s>a" },   -- one key or a list
  popup = "layer",                       -- "layer" (floating) or "split"
  bin = "/home/me/.local/bin/rex-rollcall",  -- default: where install ran from
  deadline_ms = 1000,                    -- budget for all hosts to answer
  past_limit = 200,                      -- past rows per host and source
  vim = false,                           -- true: open in Vim-style normal mode
  sources = {},                          -- see below
}
```

The popup falls back to a split if the floating layer can't open.
`rex do rollcall popup=split` opens a split once.

## Keys

| Key | Does |
| --- | --- |
| enter | live row: focus the pane. Past row: resume in a split beside the pane you came from (a new session on another host) |
| ctrl-t | resume in a new tab |
| ctrl-y | copy the resume command (OSC 52) |
| ctrl-x | close the live pane |
| ctrl-u | unlock source previews (see Sources) |
| tab | cycle all / live / past |
| ctrl-o | cycle the host filter |
| ctrl-s | show or hide short past sessions |
| ctrl-r | ask every host again |
| ctrl-/ | show every key |

Short past sessions (fewer than 5 messages, like one-shot probes) are hidden by
default. The label above the list says how many are hidden.

### Vim mode

With `vim = true` (or `rex-rollcall --vim` from a shell), the picker opens in
normal mode. The prompt's `/` is dim in normal mode and bright while you search.

| Key | Does |
| --- | --- |
| j / k | move down / up |
| d / u, ctrl-d | half a page down / up |
| g / G | first / last row |
| / or i | search; any other letter also starts a search |
| esc | in search: back to normal mode, keeping the filter. In normal mode: quit |
| q | quit |
| ? | show every key |

The ctrl keys above work in both modes. ctrl-u stays unlock, so half a page up is `u`.

The picker ignores `FZF_DEFAULT_OPTS` and uses only the terminal's 16 ANSI colors, so it
follows your terminal or Rex theme.

The preview shows the end of the pane's screen for live rows and the last messages
of the transcript for past rows.

For screenshots and demos, `rex do rollcall demo=true` (or `rex-rollcall --demo`)
opens the picker with made-up sessions and previews. It reads no real sessions, and
keys that would jump, resume, close or unlock do nothing.

If a key does nothing while Claude Code has focus, run `rex-rollcall doctor --keys`
for a short repro.

## MCP server

`rex-rollcall mcp` serves the same sessions to an agent over MCP (stdio). For
Claude Code:

```sh
claude mcp add rollcall -- rex-rollcall mcp
```

| Tool | Does |
| --- | --- |
| `sessions_list` | list live and past sessions; filter by `host`, `agent`, `state` (all, live, past), words in the title |
| `sessions_search` | full-text search over past transcripts on every host, through each host's Wake index |
| `session_show` | the last messages of one past transcript |
| `session_open` | the command that resumes or focuses a session; it does not run it |

Every host must run the same rex-rollcall version, because each host answers for
itself. Every title, excerpt, and transcript goes through the same redaction as
source previews. Source rules still hold: a `"never"` source is not searched or
shown, and an `"unlock"` source answers only while its host is unlocked. The
source side enforces that, not the MCP server.

## How it works

Each Rex host answers for itself. The picker runs one
`rex -S HOST do rollcall_op` per host from `rex hosts`, all in parallel, under one
deadline. On the host, the `rollcall_op` action (from the Lua module) collects that
server's panes in-process and pipes them to `rex-rollcall _op`. That process detects
agents, reads the local Wake index read-only, and runs any sources. Hosts that don't
answer in time show as a warning. Press ctrl-r to try again.

The module and binary carry a version. A mismatch is refused with "rerun rex-rollcall
install", so the two can't drift silently.

## Sources: root and containers

A source is another account or container on the same host. `wrap` is an argv
prefix that runs a command as or in that target. `bin` is the path of rex-rollcall
there. No shell reads either.

```lua
sources = {
  { name = "root", previews = "unlock",
    wrap = { "sudo", "-n", "-u", "root" }, bin = "/usr/local/bin/rex-rollcall" },
  { name = "hermes", user = "root", previews = "never",
    wrap = { "sudo", "-n", "/usr/sbin/pct", "exec", "101", "--" }, bin = "/usr/local/bin/rex-rollcall" },
}
```

Listing runs exactly `WRAP... BIN _op --source-only`. With `--source-only` the
binary ignores any sources or panes in its request, and only reads. So a sudoers
rule can allow that one argv without a password and grant no shell:

```
me ALL=(root) NOPASSWD: /usr/local/bin/rex-rollcall _op --source-only
me ALL=(root) NOPASSWD: /usr/sbin/pct exec 101 -- /usr/local/bin/rex-rollcall _op --source-only
```

`bin` must be owned by root and not writable by you. A NOPASSWD rule on a file you
can overwrite is a root shell.

### What a source shows

A source lists metadata only: agent, title, folder, time. The source side always
cuts titles to 60 characters and redacts anything token-shaped, in titles and in
previews: API keys (`sk-…`, `sk-ant-…`, `AKIA…`, `AIza…`), GitHub, GitLab, Slack
and Tailscale tokens, JWTs, private key blocks, and long values after `password`,
`secret`, `token` or `api_key`.

Transcript previews depend on `previews`:

| `previews` | Preview |
| --- | --- |
| `"never"` (default) | never; the picker doesn't ask |
| `"unlock"` | only while the source host is unlocked |

The source side enforces the unlock, not the picker. It shows a transcript only while
`/run/rex-rollcall/unlocked-until` is a root-owned `0600` file holding a future
time, at most 30 minutes away. Inside a container that file never exists, so
`"never"` holds there even if the picker asks. Every unlock and every source preview
is logged to the journal (`journalctl -t rex-rollcall`).

To unlock:

- `rex-rollcall unlock 10m` runs `sudo /usr/local/bin/rex-rollcall _unlock 10m` and
  asks for your password.
- Press ctrl-u in the picker. It runs the `rollcall_unlock` action, which can use
  Touch ID (below). A locked preview says "Previews are locked. Press ctrl-u to unlock them."

### Touch ID unlock

Put a key in 1Password and allow it, in root's `authorized_keys`, to run only the
unlock:

```
restrict,command="/usr/local/bin/rex-rollcall _unlock 10m" ssh-ed25519 AAAA… rex-rollcall-unlock
```

The examples below call the host with the sources `pve` and the laptop `mac`.
On the Mac, `setup{}` defines `rollcall_unlock`, which runs ssh with that key
through the 1Password agent. 1Password asks for Touch ID:

```lua
unlock = {
  host = "pve", user = "root",
  identity_file = "~/.ssh/rex-rollcall-unlock.pub",
  identity_agent = "~/Library/Group Containers/2BUA8C4S2C.com.1password/t/agent.sock",
},
```

On the host with the sources, point ctrl-u at the Mac's Rex host
label instead: `unlock = { via = "mac" }`. Touch ID has to finish within the
25-second limit that the Rex action gets.

### Resuming and setup

Resuming a source row runs the same wrap without `-n` in a new pane, for example
`sudo -u root sh -lc 'cd "$1" && shift && exec "$@"' sh /root claude --resume ID`.
sudo asks for your password in that pane. No password-free path runs anything
except the read-only listing and the forced-command unlock.

Each target needs its own Wake index. `contrib/proxmox-sources-setup.sh` sets up both
sources on a Proxmox host:

- the root-owned binary on the host and in the container
- the Wake index and refresh timer
- the sudoers rule
- the `/run/rex-rollcall` tmpfiles entry
- the `authorized_keys` line it prints for you to add

Sources never produce live rows; a service has no pane.

The source binary must be the same version as yours. Rerun the setup after an upgrade.

## Agents

One table in `internal/agent/agent.go`: glyph, color, Wake id, process names, resume
argv, and an optional live-id lookup. Adding an agent is one entry.

## Uninstall

```sh
rm ~/.config/rex/rex_rollcall.lua ~/.local/bin/rex-rollcall
```

Remove the `require("rex_rollcall")` lines from init.lua and run `rex config reload`.
For sources, also remove `/usr/local/bin/rex-rollcall`, `/etc/sudoers.d/rex-rollcall`,
`/etc/tmpfiles.d/rex-rollcall.conf` and the `authorized_keys` line.

## License

MIT
