# Security

## Reporting a problem

Please open a private security advisory on GitHub
(<https://github.com/aspidovss/spmag-sony-pm-alt-gui/security/advisories/new>)
or an issue without exploit details. This is a hobby project written with AI
help (see the README); there is no bug bounty.

## What the program trusts

| Party | Trust |
|---|---|
| The camera (a device on your Wi-Fi) | Needed to receive photos. The PTP/IP protocol of Sony cameras has **no encryption and no authentication of the camera**: anyone on the same network can announce an `ILCA…` camera. Everything the camera sends (file names, sizes, texts) is therefore treated as untrusted. |
| The web interface user | Whoever may open the page can start downloads, change the save folder and (on Linux) write the pairing ID to a camera. It is protected by a login and password and is off by default. |
| Your computer's other programs | On the same computer an unprotected local interface (`127.0.0.1`, no password) can be reached by any program. Set a password if that matters. |

## Review of version 0.4.1 (what was checked and fixed)

Checked by reading the code, by new automated tests (every fix below has a test
that fails without it), by a black-box attack script against the running
program (32 checks), and with `go vet` and the race detector. Static scanners
(`gosec`, `govulncheck`) could not be installed in the review environment;
`govulncheck` now runs in CI (informational).

**Fixed**

1. **Writing outside the save folder (high).** The file name reported by the
   camera was joined to the save path unchecked. A device pretending to be a
   camera could save a file as `../../x` (or `..\..\x`, an absolute path, `..`)
   and so write anywhere the user can write. Names are now reduced to a plain
   file name (no folders, no control or reserved characters, no Windows device
   names, no trailing dots), and the final path is verified to be inside the
   save folder.
2. **Anyone could be a "camera".** Added the *allowed cameras* list (IP address,
   network, serial number, model). Addresses are checked before anything is sent
   to the device, serial and model right after connecting and before any file
   list is read. A list with a typo refuses everything instead of allowing all.
   Without a list, an address typed by hand must be a local one (no public
   addresses), which also stops the web page from being used to make the
   program connect to arbitrary hosts.
3. **Password guessing.** After 5 wrong logins from one address the web
   interface answers `429` for a minute, even to the right password. Failed
   attempts are also delayed.
4. **Resource limits of the web server.** Read, write and idle timeouts, a
   16 KB header limit, at most 128 simultaneous connections, 1 MB request
   bodies, log lines capped at 2000 bytes.
5. **Text from the network** (camera name, file names) had control characters
   and line breaks removed before it reaches logs and screen, so a device cannot
   forge log lines. The page already used `textContent` (no HTML injection) and a
   strict Content-Security-Policy (no inline script).
6. **Redirects.** Fetching the camera's description no longer follows
   redirects, so a device cannot send the program to another host.
7. **Memory.** A PTP/IP packet that announces 60 MB no longer reserves 60 MB
   before data arrives.
8. `settings.json` (it can contain the web password) is written readable only
   by its owner. Extra isolation headers were added to the web pages.
9. A race when settings were saved twice in a row (the second read could see the
   old values) was fixed.

**Already in place** (and re-tested): Host-header check for the unprotected
local mode (DNS rebinding), a required custom header and same-origin check on
every changing request (cross-site requests), constant-time credential
comparison, validation of the save folder, subfolder pattern and IP address,
no directory listing or path traversal for the embedded page.

## Re-check of version 0.4.4

Done after the per-camera records, the `{serial}` folder token and the move to
Go 1.27.2 (no vulnerabilities reported by `govulncheck` v1.9.0 in CI):

* The web server was attacked again with the same 32 checks as in 0.4.1
  (authentication, cross-site requests, path tricks, oversized headers, bodies
  and URLs, slow connections, a 140-connection flood, password guessing,
  connect-by-IP and allowed-cameras rules). All passed.
* The camera's serial number and model go into folder names (`{serial}`,
  `{camera}`). Tests with hostile values (`..`, `..\..\x`, absolute paths,
  NUL) show that the folder never leaves the save folder.
* Records of received files are now per camera. A record that belongs to
  another camera is never taken over, so one camera cannot make another
  camera's files look "received" (tested).
* Hardening of the packaging: the systemd unit now also forbids kernel,
  clock and cgroup changes, namespaces, real-time scheduling, other address
  families than IP/netlink/unix and writable executable memory, and drops all
  capabilities (so the web port must be above 1023); the docker-compose
  example runs with a read-only root file system, no capabilities and
  `no-new-privileges`. The CI token of the build workflow is read-only.
  These settings were checked for syntax only (`systemd-analyze verify`,
  `docker compose config`), not on a real server.
* Not done: `gosec` could not be run; the release workflow uses a
  third-party action (`softprops/action-gh-release`) pinned to a version tag,
  not to a commit; the Docker image runs as root inside the container.

## Known limits (not fixed, by design or by protocol)

* The web interface uses **plain HTTP with Basic authentication**: the password
  is visible to anyone who can capture traffic on the network. Use it on a
  trusted LAN or behind a TLS reverse proxy (Caddy, nginx), or keep it on
  `127.0.0.1`.
* The web password is stored **in clear text** in `settings.json` (it must be
  shown in the Windows settings window). The file is private to your account on
  Linux; on Windows it is protected by your user profile only.
* A logged-in web user can choose any save folder the program may write to. On a
  server run the program as an unprivileged user (the `.deb` does) and give it
  only the photo folder.
* An attacker on your network who knows a camera's IP address and serial number
  can still imitate it. Use the list together with a network you control
  (separate Wi-Fi for the camera, router access control).
* Files are saved as the camera describes them; their content is not scanned.

## Privacy: no telemetry, nothing sent to the internet

The program has no telemetry, no update check, no analytics and no online
service. Its only network activity is local: SSDP discovery on the local network,
the connection to the camera (PTP/IP, port 15740), the optional web interface
you turn on yourself, and (for the camera's description) one plain `GET` to the
address the camera announces, without redirects. Photos, settings and the list of
received files stay on your computer. You can check this in the source
(`internal/ssdp`, `internal/ptpip`, `internal/web`) or with a firewall: the
program needs no outbound access to the internet.
