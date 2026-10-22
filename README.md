# Stockpile Print Helper

A small, open-source program for a computer at a shop. It lets Stockpile print
receipts, shipping labels and packing slips on that computer's printers
without a print window each time.

It lives in the Stockpile repository for now (`apps/print-helper/`). It moves
to its own public repository once the publishing details are settled. It is
Go with no third-party modules and no cgo: just the standard library and
Windows' own DLLs.

## What it does

- Connects **out** to Stockpile over HTTPS. That is one connection, held open
  for a long poll.
- Accepts one instruction: **"print this document on printer X."** X is a
  printer from its own list.
- Prints from memory and keeps no copy. Windows' print queue holds a job
  only while it prints.
- Tells Stockpile only these things:
  - which printers it can use, and whether each is ready
  - how each job went
  - its version and operating system
  - that it's still there (each poll)
- Looks for printers on the network **only when someone runs "Find
  printers"**. Nothing it finds is used until a person keeps it.

## What it never does

- Run commands, scripts or programs sent from Stockpile. The program has no
  feature that could. Label data does carry printer instructions, such as
  where to print a barcode, because that is how label printers work. The
  helper refuses the ones that change a printer for good (SECURITY.md).
- Read, upload or look at your files.
- Accept connections. It opens no ports and needs no router or firewall
  changes. If you limit outgoing traffic, allow Stockpile's helper host on
  port 443, plus your printers.
- Send usage tracking, analytics or crash reports.
- Print for anyone but the one shop and location it's paired with.
- Read PDFs. Stockpile turns a PDF into PNG pages on its own servers, and
  the helper decodes only PNGs, with Go's own decoder.

## Where it connects

- Stockpile's helper host, `https://helper.<domain>/api/helper/v1`. The
  requests are `pair`, `hello`, `jobs/next`, `jobs/:id/take`,
  `jobs/:id/result` and `unpair` (`internal/api`).
- The printers on its list: printers installed in Windows, and network
  printers someone kept here. A network printer must be on the shop's own
  network (10.x, 172.16–31.x, 192.168.x, IPv6 ULA or link-local), on port
  9100–9109 or 631. Those rules are checked again on every connect
  (`internal/netpolicy`).

## Pairing, Remove, unpair

- The shop's owner makes a one-time code in **Settings › Printing**. It is
  valid for 10 minutes, works once, and is for one location. Pairing with
  it returns a device token once. Only its SHA-256 is kept in Stockpile.
- On Windows the token is stored with DPAPI for the signed-in user. It
  can't be read by another Windows user or on another computer.
- **Remove** in Stockpile refuses every later request at once, including a
  held poll. The helper then forgets its token.
- `unpair` does the same from this computer.

## The window and the tray icon

With no command (how Windows starts it at sign-in, and from the Start menu) it
shows its window when it isn't paired, and otherwise sits by the clock as an
"S" with a dot (green connected, amber paused, red can't reach Stockpile,
grey not paired). Click the icon to open the window; right-click for Open,
Pause/Resume printing and Quit. Closing the window keeps it running.

The window (`internal/ui`, Windows' own controls through `syscall` — no UI
toolkit) shows the connection, Pair (not paired), Pause printing / Resume,
the printers it can use, Find printers and Add by IP, recent jobs, Unpair
(with a confirm), the version and the log folder. It opens no browser and
starts no program: links are shown as text. A second copy just brings the
first one's window up. Everything goes through `internal/app`, the same
steps as the commands below. `--ui-selftest FILE` lists what the window
shows and closes (for tests).

## Commands

```
stockpile-print-helper            the window and tray icon
stockpile-print-helper pair --server https://YOUR-STOCKPILE CODE
stockpile-print-helper run
stockpile-print-helper status
stockpile-print-helper printers
stockpile-print-helper find-printers
stockpile-print-helper add-printer 192.168.1.50[:9100] [--kind label|receipt|page] [--name NAME]
stockpile-print-helper remove-printer 192.168.1.50[:9100]
stockpile-print-helper pause | resume
stockpile-print-helper unpair
```

`--config DIR` sets the helper's folder. By default it is
`%LOCALAPPDATA%\StockpilePrintHelper`, inside the MSIX package's own storage
once it's packaged. The folder holds:

- `device.bin` (the token, protected)
- `settings.json`
- `journal.jsonl` (job ids, titles, printers and results, kept 7 days)
- `logs/` (7 days, plain text, never document contents)

## Packaging (MSIX)

`packaging/AppxManifest.xml` is the package: the program as a full-trust
desktop app (`runFullTrust`) with a `windows.startupTask` (it starts at
sign-in, as the signed-in user — no service, no administrator; the user can
turn it off in Windows Settings › Apps › Startup). No other capability.

```
GOOS=windows CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -H=windowsgui -X main.Version=1.0.0 -X main.Server=https://helper.<domain>" -o bin/StockpilePrintHelper.exe ./cmd/stockpile-print-helper
go run ./cmd/mkassets bin/assets
powershell -File packaging/build-msix.ps1 -Version 1.0.0.0 -Exe binStockpilePrintHelper.exe -Assets binassets -Out binStockpilePrintHelper.msix
```

The package is unsigned: the Microsoft Store signs it when it's published.
Identity Name and Publisher come from the Store reservation; until the
publishing account is settled the script uses development values. The
script prints the program's SHA-256 for the release notes.

## How it prints

| Printer | Labels (ZPL) | Pages (receipts, slips) |
|---|---|---|
| Installed in Windows, ZPL driver (ZDesigner …) | through the spooler, RAW datatype | GDI `StretchDIBits`, at the page's size on paper |
| Installed in Windows, other (EPL Zebras, receipt, office) | not offered | GDI, as above |
| Kept network printer, Zebra (answers `~HI`) | port 9100: `~HS` status first (paper out, paused or head open means nothing is sent), then the bytes | not offered |

A Windows job is watched for up to a minute:

- **printed:** the spooler said so
- **sent:** it left the queue, or is still printing at the minute
- **failed:** stuck on paper out or an error for 20 s. The job is
  **cancelled**, so it never prints later and "Print again" is safe.

Every step is written to the journal before the next one. After a crash, a
job that may have reached a printer is reported **uncertain**, and one that
didn't is reported **not received**. Neither is ever reported as printed.

## Build and test

You need Go 1.24 or later. Without a local Go, use Docker:
`docker run --rm -v "$PWD:/src" -w /src golang:1.24-bookworm …`.

```
go vet ./... && GOOS=windows go vet ./...
go test -race ./...
GOOS=windows CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.Version=1.0.0" -o stockpile-print-helper.exe ./cmd/stockpile-print-helper
```

**End-to-end test.** This needs a local Stockpile api and worker running,
and a development build (`-tags devsink`, which adds a "File printer" that
writes jobs to a folder; release builds can't write a document):

```
GOOS=windows CGO_ENABLED=0 go build -tags devsink -o bin/stockpile-print-helper-dev.exe ./cmd/stockpile-print-helper
STOCKPILE=http://localhost:8071 ORIGIN=http://localhost:5191 OWNER_PASSWORD=… HELPER=bin\stockpile-print-helper-dev.exe node e2e/run.mjs
```

It runs these steps in order:

1. Pairs (the same code a second time is refused).
2. Keeps a fake raw Zebra on this machine's own network address.
3. Runs the helper.
4. Has the owner pick its printers.
5. Prints a test page to each: the fake Zebra gets the ZPL, and the file
   printer gets a PNG.
6. Pauses at the helper. The next print fails at once with "… is paused".
7. Removes the helper in Stockpile. The running helper stops and forgets its
   token.

This folder is not an npm workspace (it has no `package.json`), and the Node
CI doesn't build it.
