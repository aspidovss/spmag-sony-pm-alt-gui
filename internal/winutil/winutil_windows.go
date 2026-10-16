//go:build windows

// Package winutil contains small Windows helpers: autostart, elevation,
// firewall rule, single instance and process detection.
package winutil

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// SetAutostart adds or removes the program from HKCU\...\Run.
func SetAutostart(name string, enable bool, args string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !enable {
		err := k.DeleteValue(name)
		if err == registry.ErrNotExist {
			return nil
		}
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return k.SetStringValue(name, fmt.Sprintf(`"%s" %s`, exe, args))
}

// IsElevated reports whether the process runs with admin rights.
func IsElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

var (
	shell32         = windows.NewLazySystemDLL("shell32.dll")
	shellExecuteExW = shell32.NewProc("ShellExecuteExW")
)

type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         uintptr
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     uintptr
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    uintptr
	dwHotKey     uint32
	hIcon        uintptr
	hProcess     windows.Handle
}

// RunElevated starts this executable with args via the UAC prompt, waits
// for it and returns its exit code.
func RunElevated(args string) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return -1, err
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	params, _ := windows.UTF16PtrFromString(args)
	sei := shellExecuteInfo{
		fMask:        0x00000040, // SEE_MASK_NOCLOSEPROCESS
		lpVerb:       verb,
		lpFile:       file,
		lpParameters: params,
		nShow:        0, // SW_HIDE
	}
	sei.cbSize = uint32(unsafe.Sizeof(sei))
	r, _, e := shellExecuteExW.Call(uintptr(unsafe.Pointer(&sei)))
	if r == 0 {
		if e == windows.ERROR_CANCELLED {
			return -1, fmt.Errorf("the administrator prompt was cancelled")
		}
		return -1, e
	}
	if sei.hProcess == 0 {
		return 0, nil
	}
	defer windows.CloseHandle(sei.hProcess)
	if _, err := windows.WaitForSingleObject(sei.hProcess, windows.INFINITE); err != nil {
		return -1, err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(sei.hProcess, &code); err != nil {
		return -1, err
	}
	return int(code), nil
}

// AddFirewallRule allows inbound SSDP (UDP 1900) from the local subnet for
// this executable. Needs admin rights.
func AddFirewallRule(ruleName string, oldNames ...string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	for _, n := range append([]string{ruleName}, oldNames...) {
		del := exec.Command("netsh", "advfirewall", "firewall", "delete", "rule", "name="+n)
		del.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		_ = del.Run()
	}
	add := exec.Command("netsh", "advfirewall", "firewall", "add", "rule",
		"name="+ruleName, "dir=in", "action=allow", "program="+exe,
		"protocol=UDP", "localport=1900", "remoteip=localsubnet", "profile=any", "enable=yes")
	add.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := add.CombinedOutput()
	if err != nil {
		return fmt.Errorf("netsh: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// SingleInstance returns false if another instance holds the named mutex.
func SingleInstance(name string) bool {
	p, _ := windows.UTF16PtrFromString("Local\\" + name)
	_, err := windows.CreateMutex(nil, false, p)
	return err != windows.ERROR_ALREADY_EXISTS
}

var (
	user32              = windows.NewLazySystemDLL("user32.dll")
	findWindowW         = user32.NewProc("FindWindowW")
	showWindow          = user32.NewProc("ShowWindow")
	setForegroundWindow = user32.NewProc("SetForegroundWindow")
)

// ActivateWindow brings an existing top-level window with the given title
// to the front. Returns false if none was found.
func ActivateWindow(title string) bool {
	t, _ := windows.UTF16PtrFromString(title)
	h, _, _ := findWindowW.Call(0, uintptr(unsafe.Pointer(t)))
	if h == 0 {
		return false
	}
	showWindow.Call(h, 9) // SW_RESTORE
	setForegroundWindow.Call(h)
	return true
}

// RunningProcesses returns executable names of running processes whose
// name starts with one of the prefixes (case-insensitive).
func RunningProcesses(prefixes ...string) []string {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	var out []string
	seen := map[string]bool{}
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		name := windows.UTF16ToString(pe.ExeFile[:])
		ln := strings.ToLower(name)
		for _, p := range prefixes {
			if strings.HasPrefix(ln, strings.ToLower(p)) && !seen[ln] {
				seen[ln] = true
				out = append(out, name)
			}
		}
	}
	return out
}

// OpenFolder opens Explorer at path (selecting the file if path is a file).
func OpenFolder(path string) {
	st, err := os.Stat(path)
	var cmd *exec.Cmd
	if err == nil && !st.IsDir() {
		cmd = exec.Command("explorer.exe", "/select,", path)
	} else {
		cmd = exec.Command("explorer.exe", path)
	}
	_ = cmd.Start()
}

// OpenURL opens a web link in the default browser.
func OpenURL(u string) {
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		return
	}
	_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
}
