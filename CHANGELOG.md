# Changelog

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
