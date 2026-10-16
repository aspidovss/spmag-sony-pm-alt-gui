# Camera compatibility

SPMAG Wifi Photo Receiver speaks the protocol of the camera menu item
**MENU → Wireless → Send to Computer** (PTP/IP, the same one PlayMemories Home
uses). Sony does not publish a list of cameras that have this item: its help
pages only say "models that support the Wi-Fi import function of PlayMemories
Home" and "look for the Wi-Fi logo". So everything below except the first row
is **theoretical** — an educated guess from the era and feature set of each
camera. If you test a camera, please report it (see the end of this page).

| Status | Meaning |
|---|---|
| ✅ Verified | Tested with this program on a real camera |
| 🟡 Reported | Works with another open-source implementation, not tested with this program |
| 🔵 Likely | Wi-Fi camera from the PlayMemories Home era (2012–2018) that has "Send to Computer"; same protocol expected |
| ❔ Uncertain | May have the menu item, but newer firmware or software generation; unknown |
| ⛔ Unlikely | Sony states that PlayMemories Home is **not** supported; these cameras use Imaging Edge Mobile or FTP |

## Alpha interchangeable-lens cameras

| Camera | Status |
|---|---|
| ILCA-77M2 (α77 II) | ✅ Verified (firmware 2.0, 518 files, ~2.9 MB/s) |
| ILCE-5000 (α5000) | 🟡 Reported (tested with sony-pm-alt) |
| ILCE-5100 (α5100), ILCE-6000 (α6000) | 🔵 Likely |
| NEX-5R, NEX-5T, NEX-6 | 🔵 Likely (the first cameras with this feature) |
| ILCE-7 (α7), ILCE-7R (α7R), ILCE-7S (α7S) | 🔵 Likely |
| ILCE-7M2 (α7 II), ILCE-7RM2 (α7R II), ILCE-7SM2 (α7S II) | 🔵 Likely |
| ILCA-99M2 (α99 II) | 🔵 Likely |
| ILCE-6300 (α6300), ILCE-6500 (α6500) | ❔ Uncertain |
| ILCE-6400 (α6400), ILCE-6600 | ❔ Uncertain |
| ILCE-7M3, ILCE-7RM3, ILCE-9, ILCE-9M2, ILCE-7RM4 | ❔ Uncertain (FTP / Imaging Edge generation) |
| ILCE-1, ILCE-1M2, ILCE-9M3, ILCE-7M4, ILCE-7M5, ILCE-7RM5, ILCE-7RM6, ILCE-7SM3, ILCE-7C, ILCE-7CM2, ILCE-7CR, ILCE-6700 | ⛔ Unlikely (named by Sony as not supported by PlayMemories Home) |
| ZV-E1, ZV-E10, ZV-E10M2, ZV-1F, ZV-1M2, ILME-FX3, FX30, FX6 | ⛔ Unlikely (same) |

## Cyber-shot and other cameras

| Camera | Status |
|---|---|
| DSC-RX100M2 … RX100M5A, DSC-RX10M2 / M3 / M4, DSC-RX1RM2 | ❔ Uncertain — Wi-Fi models of the same era; some may use a different flow |
| DSC-HX and DSC-WX models with Wi-Fi, DSC-RX0 / RX0M2 | ❔ Uncertain |
| Handycam (HDR-/FDR- with Wi-Fi), Action Cam (HDR-AS, FDR-X) | ❔ Uncertain — mostly import movies; this program is built for photos |

## Limits that also apply to PlayMemories Home

- The memory card must be inserted; internal memory is not transferred.
- Cameras need the card's files in the standard folders; XAVC S videos are not
  sent by the original software either.
- Photos and movies are listed, but camera-side markers in movies are not kept.

## How to test your camera and help the list grow

1. Pair the camera (see the README: USB pairing, or an existing PlayMemories Home pairing).
2. Run `wpr capture` while the camera is in **Send to Computer**. It saves a small
   set of protocol answers (model, storage, a few file records) into
   `testdata/<model>/`; the camera's serial number, MAC address and IP are masked.
3. Open an issue or pull request with that folder and the camera's model and firmware version.

*Sources: Sony support article "Wi-Fi import with PlayMemories Home" (lists models
that PlayMemories Home does not support), Sony PlayMemories Home Help Guide, the
sony-pm-alt README (ILCE-5000), and this project's own tests.*
