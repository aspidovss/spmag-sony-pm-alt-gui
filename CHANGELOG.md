# Changelog

## v0.4.1

Security release. Please update, especially if the program runs on a network you
do not fully control. Details: [SECURITY.md](SECURITY.md).

- **Fixed: a device on the network posing as a camera could write files outside
  the save folder** through the file names it reports (`../../x`, `..\..\x`,
  absolute paths). Names are now reduced to a plain, safe file name and the
  final path is checked.
- **New: allowed cameras list** (Settings, web page, `wpr watch -allow`): IP
  addresses, networks (`192.168.1.0/24`), `serial:NUMBER`, `model:NAME`. Other
  devices are ignored and never receive anything. A list with a typo refuses
  everything. Without a list, an IP typed by hand must be a local address.
- Web interface: after 5 wrong passwords an address is blocked for a minute;
  request timeouts, header and connection limits; log lines capped; more
  isolation headers.
- Text from the network is stripped of control characters before it reaches
  logs and screen; SSDP descriptions are fetched without redirects; a large
  PTP/IP packet no longer reserves memory before its data arrives.
- Fixed a race where settings saved twice quickly could be read back stale.
- CI runs `govulncheck` (informational).

## v0.4.0

- **AppImage** for Linux (amd64 and arm64): one file, no installation. Started
  without arguments it runs the receiver, opens the browser interface for this
  computer and offers an *Exit* button; with arguments it works like `wpr`
  (`wpr watch -ui` does the same from the command line). A second start just
  opens the page again.
- **Web interface settings** in the Windows program and on the Linux page:
  turn it on or off, port, login, password, and whether other computers may
  connect (a password is then required). Changes apply when you save, no
  restart; the page follows a changed port. Available in all 10 languages.
- The web interface now exists in the Windows program too (same page, same
  API). Command-line options (`-web`, `-web-user`, `SPMAG_WEB*`) still take
  precedence and make the page's web options read-only.
- `settings.json` is now written readable only by its owner (it can hold the
  web password).

## v0.3.0

- **Browser interface** (`wpr watch -web ADDR`, also in Docker): status, speed and
  time left, file list, download / stop / re-check / finish, settings, log, USB
  pairing, 10 languages, phone-friendly. Password (HTTP Basic) is required on
  network addresses; local-only mode checks the Host header; changing requests
  are protected against cross-site use.
- **USB pairing on Linux** through `/dev/sg*` (`wpr usb`, `wpr pair`, web page).
  Ported from the Windows code and covered by unit tests; not yet tested with a
  real camera.
- **`.deb` packages** for amd64 and arm64 (systemd service, `spmag-wpr` user,
  `/etc/default/spmag-wpr`).
- Docker: `SPMAG_WEB` / `SPMAG_WEB_PASSWORD` environment variables;
  docker-compose requires a password.
- `wpr watch -lang` chooses the language of the web interface and status texts.

## v0.2.0

- Linux: `wpr watch` service mode (automatic receive, finish, wait again),
  systemd unit, Dockerfile and docker-compose example.
- Linux builds of `wpr` for amd64 and arm64 in every release.
- `wpr fakecam`: realistic file sizes and Wi-Fi speed (`-jpeg`, `-raw`, `-rate`)
  for demos; `SPMAG_CONFIG_DIR` runs the app with a separate data folder.
- Screenshots in README.
- Interface languages: English, 简体中文, Español, Français, Deutsch, Português (Brasil),
  Русский, 日本語, 한국어, Italiano (Settings → Language; default is the Windows
  display language). Logs and the CLI stay in English.
- Theoretical camera compatibility list (docs/COMPATIBILITY.md).
- CI: updated GitHub Actions (Node 24), Ubuntu 24.04 runners.

## v0.1.0

First public release of **SPMAG Wifi Photo Receiver**.

- Automatic discovery of Sony cameras in "Send to Computer" mode (SSDP).
- PTP/IP transfer with the Sony start/finish handshake; the camera shows the
  result and switches off after an automatic transfer.
- Reconciliation of camera files with the PC: received / missing / failed /
  excluded by filter; download all missing or selected files.
- Live transfer speed and estimated time left for the whole run.
- File type filter (JPEG/HEIF, RAW, video, other) and date subfolders.
- Pairing over USB (Mass Storage mode) and reuse of an existing
  PlayMemories Home pairing.
- Tray icon, autostart, notifications, firewall rule helper.
- `wpr` command-line tool: discover, list, pull, pair, capture, fakecam.
- Tested with Sony α77 II (ILCA-77M2), firmware 2.0.
