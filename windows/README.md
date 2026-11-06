# hamnews on Windows

INN does not run natively on Windows (it needs fork, Unix-domain sockets, and a
setuid port binder), so the news server is the `hamnews` Debian package running
inside WSL2 Ubuntu. Everything that touches the radio or the user runs natively on
Windows: `hnlink.exe`, direwolf, FLAMP and fldigi, and your newsreader. WSL forwards
`localhost:119` to Windows, so Thunderbird and `hnlink.exe` reach the server.

Copy this whole folder to the Windows machine. Put the net's group list in
`groups.txt`, then in a normal (non-admin) PowerShell:

```bat
install-windows.cmd -Call kn6fln -From "KN6FLN <you@example.com>"
```

The script:

- checks `hnlink.exe` and the `.deb` against `SHA256SUMS`
- installs Ubuntu if it is missing (create the Linux user, then run it again)
- turns on systemd in it
- preseeds the callsign through debconf and `apt-get install`s the `hamnews` .deb
  and tin, then installs `groups.txt` as `/etc/hamnews/groups`
- leaves Windows FLAMP's settings alone (`-DisableFlampAutoSave` turns off *Auto save rx
  files*, keeping a `.bak-hamnews` backup; see OPERATIONS.md §2.1)
- installs `hnlink.exe` and a `hamnews` command into `%LOCALAPPDATA%\hamnews`, on your
  PATH
- registers logon tasks:
  - `hamnews` keeps the distribution running (`-NoAutoStart` skips it)
  - `hnlink` runs the exchange as `CALL-5` via KISS `127.0.0.1:8001`
    (`-Ssid`, `-Kiss`; `-NoLink` skips it)

Re-running it is safe: apt upgrades the package and keeps your edits in
`/etc/hamnews`.

Then, from a new terminal:

| Command | What it does |
|---|---|
| `hamnews page` | status page: stations heard, transfers, messages, posting |
| `hamnews read` | tin inside WSL (or Thunderbird: news server `localhost`) |
| `hamnews send [-Days N]` | FLAMP: write `%USERPROFILE%\NBEMS.files\FLAMP\tx\<call>-YYYYMMDD.rnews` |
| `hamnews receive` | FLAMP: load every `*.rnews*` under `NBEMS.files\FLAMP\rx\` |
| `hamnews status` | news server, hnlink task, recent hnlink log |
| `hamnews start` | bring both back after `wsl --shutdown` |

`itsjustnews.exe` is the receive-only monitor (OPERATIONS.md §4.1), and `airtime.exe`
the channel accounting (§4.2, needs direwolf's AGW port); run either from any
terminal while direwolf is up.

Windows stations run the stock upstream FLAMP. With auto-save on, received files land
in `FLAMP\rx\` and `hamnews receive` picks them up.

**Untested on real Windows:**
- The PowerShell has been checked by reading only.
- Two things depend on WSL's localhost forwarding: hnlink.exe reaching innd, and
  innd treating those IHAVE connections as local (`incoming.conf`).
- If `hnlink load` reports IHAVE refused, add the address it connects from to
  `/etc/hamnews/incoming.conf` in WSL; `journalctl -u hamnews` there shows it.
