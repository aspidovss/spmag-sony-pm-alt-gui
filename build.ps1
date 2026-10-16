# Builds the Windows executables into dist\ (run from the repository root).
# Requires Go 1.22+:  winget install GoLang.Go
$ErrorActionPreference = "Stop"
# -mod=mod lets Go fill in missing go.sum entries on the first build.
if (-not $env:GOFLAGS) { $env:GOFLAGS = "-mod=mod" }
$version = if ($env:VERSION) { $env:VERSION } else { "0.1.0" }
go install github.com/akavel/rsrc@latest
$rsrc = Join-Path (go env GOPATH) "bin\rsrc.exe"
go run ./tools/genres
& $rsrc -manifest build/app.manifest -ico build/app.ico -arch amd64 -o cmd/WifiPhotoReceiver/rsrc_windows_amd64.syso
& $rsrc -ico build/app.ico -arch amd64 -o cmd/wpr/rsrc_windows_amd64.syso
New-Item -ItemType Directory -Force dist | Out-Null
$env:CGO_ENABLED = "0"
go build -trimpath -ldflags "-s -w -H windowsgui -X main.version=$version" -o dist/SPMAG-WifiPhotoReceiver.exe ./cmd/WifiPhotoReceiver
go build -trimpath -ldflags "-s -w" -o dist/wpr.exe ./cmd/wpr
Copy-Item README.md, LICENSE dist/
Get-ChildItem dist
