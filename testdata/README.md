# Camera captures

Each folder holds the protocol answers of one camera model, recorded with

    wpr capture

(serial number, camera MAC address and IP address are masked). The tests in
`internal/ptpip` and `internal/ssdp` parse every capture found here.

| File | Content |
|---|---|
| `notify.txt` | SSDP announcement sent when "Send to Computer" starts |
| `description.xml` | UPnP device description (`LOCATION` of the announcement) |
| `initcmdack.bin` | PTP/IP Init Command Ack payload |
| `deviceinfo.bin` | PTP `GetDeviceInfo` dataset |
| `storageinfo.bin` | PTP `GetStorageInfo` dataset |
| `objectinfo_folder.bin` | PTP `GetObjectInfo` of a folder |
| `objectinfo_file.bin` | PTP `GetObjectInfo` of a file |

To add your camera: run `wpr capture`, then open a pull request with the new
folder.
