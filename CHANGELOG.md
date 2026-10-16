# Changelog

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
