// genres writes the application icon (.ico) and the Windows manifest used by
// rsrc to build cmd/WifiPhotoReceiver/rsrc_windows_amd64.syso.
//
//	go run ./tools/genres
//	rsrc -manifest build/app.manifest -ico build/app.ico -o cmd/WifiPhotoReceiver/rsrc_windows_amd64.syso
package main

import (
	"log"
	"os"
	"path/filepath"

	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/icon"
)

const manifest = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0" xmlns:asmv3="urn:schemas-microsoft-com:asm.v3">
  <assemblyIdentity version="1.0.0.0" processorArchitecture="*" name="SPMAG.WifiPhotoReceiver" type="win32"/>
  <description>SPMAG Wifi Photo Receiver</description>
  <dependency>
    <dependentAssembly>
      <assemblyIdentity type="win32" name="Microsoft.Windows.Common-Controls" version="6.0.0.0" processorArchitecture="*" publicKeyToken="6595b64144ccf1df" language="*"/>
    </dependentAssembly>
  </dependency>
  <trustInfo xmlns="urn:schemas-microsoft-com:asm.v3">
    <security><requestedPrivileges><requestedExecutionLevel level="asInvoker" uiAccess="false"/></requestedPrivileges></security>
  </trustInfo>
  <compatibility xmlns="urn:schemas-microsoft-com:compatibility.v1">
    <application>
      <supportedOS Id="{8e0f7a12-bfb3-4fe8-b9a5-48fd50a15a9a}"/>
    </application>
  </compatibility>
  <asmv3:application>
    <asmv3:windowsSettings>
      <dpiAware xmlns="http://schemas.microsoft.com/SMI/2005/WindowsSettings">true/pm</dpiAware>
      <dpiAwareness xmlns="http://schemas.microsoft.com/SMI/2016/WindowsSettings">PerMonitorV2, PerMonitor</dpiAwareness>
    </asmv3:windowsSettings>
  </asmv3:application>
</assembly>
`

func main() {
	_ = os.MkdirAll("build", 0o755)
	ico, err := icon.ICO([]int{16, 20, 24, 32, 40, 48, 64, 256}, icon.Lens)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("build", "app.ico"), ico, 0o644); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("build", "app.manifest"), []byte(manifest), 0o644); err != nil {
		log.Fatal(err)
	}
	log.Println("wrote build/app.ico and build/app.manifest")
}
