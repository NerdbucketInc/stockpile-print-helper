# Print Helper: security notes

## Reporting a vulnerability

Please report it privately through this repository's **Security** tab
("Report a vulnerability"), not in a public issue. We'll answer there.

## What the code promises

Each claim in README.md is backed by code a reviewer can check here.

| Claim | Where |
|---|---|
| No program or shell is run | No `os/exec`, `syscall.StartProcess`, `CreateProcess` or `ShellExecute` anywhere (the window shows links as text). Check with `grep -rn "os/exec\|StartProcess\|CreateProcess" .` (expect nothing). |
| No listening socket | No `net.Listen` or `ListenPacket` outside `_test.go` files. |
| Talks only to Stockpile and kept printers | `internal/api` takes https (http only to localhost, for development). `internal/netpolicy` is checked on add and on every connect. |
| No file reads beyond its own folder | `internal/config`, `internal/secret` and `internal/journal` read only inside the helper's folder. |
| Documents never written to disk | The only document writer is `sink_dev.go`, which is in **development builds only** (`-tags devsink`). Release builds use `sink_off.go`. The document buffer is cleared after printing (`agent.handle`). |
| No PDF parser | Pages arrive as PNG (`internal/pages`, Go's `image/png`), with the size checked before decoding. |
| The token is protected at rest | `internal/secret/dpapi_windows.go` uses CryptProtectData for the signed-in user, with fixed extra entropy. |
| What is reported | `api.Hello`: version, OS, paused, printers (id, name, kind, connection, formats, dpi, papers, status), and job outcomes. |
| Label data that would change a printer for good is refused | `internal/zplguard`, listed below. |

## Label commands refused (`internal/zplguard`)

The check is case-insensitive and runs on the whole job before anything is
sent.

- **Settings and resets:**
  - `^JU`: save or restore settings
  - `~JR`: reset
  - `~JN` / `~JO`: head-test checks
  - `^JZ`: reprint after error
  - `~RO`: counters
- **Network and front panel:**
  - `^NC ^NI ^NS ^NT ^ND ^NB ^NN ^NP ~NC ~NT`: network settings
  - `^KP`: front-panel password
  - `^KL`: front-panel language
  - `~WR`: wireless reset
  - `~WC`: configuration label
- **Language and prefixes:**
  - `^SZ`: printer language
  - `^CC ~CC ^CT ~CT ^CD ~CD`: prefix and delimiter changes, which could hide the rest
- **Firmware and downloads:**
  - `~DY`: files and firmware
  - `~DN`: download abort
- **Read-backs:** `^HZ`, `~HB`, `^HF`.
- **Storage that outlasts the label:** `~DG ~DB ~DU ~DS ~DT ^DF ^ID ^IS` aimed at any device but `R:` (the printer's memory, cleared at power-off). UPS labels load their logo into `R:` and delete it afterwards, and that is allowed.
- **Other languages in the same stream:**
  - SGD: `! U1 setvar`, `! U1 do`, `! U1 getvar`, `! U setvar`, `! U do`, and JSON `{}{`
  - PJL: `@PJL`
  - UEL: `ESC %-12345X`

To change the list, change `zplguard.go` and its test. `TestEveryCommandListedIsRefused` keeps the list and the check in step.
