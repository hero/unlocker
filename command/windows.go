// SPDX-FileCopyrightText: © 2014-2021 David Parsons
// SPDX-License-Identifier: MIT

package main

import (
	"fmt"
	"github.com/djherbis/times"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
	"golocker/vmwpatch"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

type VMwareInfo struct {
	BuildNumber    string
	ProductVersion string
	InstallDir     string
	InstallDir64   string
	Workstation    string
	Player         string
	KVM            string
	REST           string
	Tray           string
	AuthD          string
	HostD          string
	USBD           string
	VMXDefault     string
	VMXDebug       string
	VMXStats       string
	VMwareBase     string
	EFI32ROM       string
	EFI64ROM       string
	PathVMXDefault string
	PathVMXDebug   string
	PathVMXStats   string
	PathVMwareBase string
	PathEFI32ROM   string
	PathEFI64ROM   string
}

func amAdmin() bool {
	_, err := os.Open("\\\\.\\PHYSICALDRIVE0")
	if err != nil {
		return false
	}
	return true
}

//goland:noinspection GoUnhandledErrorResult
func copyFile(src, dst string) (int64, error) {
	println(fmt.Sprintf(" %s -> %s", src, dst))
	sourceFileStat, err := os.Stat(src)
	if err != nil {
		return 0, err
	}

	if !sourceFileStat.Mode().IsRegular() {
		return 0, fmt.Errorf("%s is not a regular file", src)
	}

	source, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer source.Close()

	destination, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	defer destination.Close()
	nBytes, err := io.Copy(destination, source)

	// Ensure timestamops are correct
	srcTimes, _ := times.Stat(src)
	_ = os.Chtimes(dst, srcTimes.AccessTime(), srcTimes.ModTime())
	_ = setCTime(dst, srcTimes.BirthTime())

	return nBytes, err
}

func delFile(src, dst string) error {
	println(fmt.Sprintf(" %s -> %s", src, dst))

	// Get chmod
	fi, _ := os.Stat(dst)
	println(fmt.Sprintf("%o", fi.Mode()))
	err := os.Chmod(dst, 666)
	if err != nil {
		return err
	}
	_, err = copyFile(src, dst)
	if err != nil {
		return err
	}
	err = os.Remove(src)
	if err != nil {
		return err
	}

	err = os.Chmod(dst, fi.Mode())
	if err != nil {
		return err
	}
	fi, _ = os.Stat(dst)
	println(fmt.Sprintf("%o", fi.Mode()))

	return nil
}

func printHelp() {
	println("usage: unlocker.exe <install | uninstall>")
	println("\tinstall - install patches")
	println("\tuninstall - uninstall patches")
}

func processID(name string) (uint32, error) {
	const processEntrySize = 568
	h, e := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if e != nil {
		return 0, e
	}
	p := windows.ProcessEntry32{Size: processEntrySize}
	for {
		e := windows.Process32Next(h, &p)
		if e != nil {
			return 0, e
		}
		if windows.UTF16ToString(p.ExeFile[:]) == name {
			return p.ProcessID, nil
		}
	}
}

func runElevated() {
	verb := "runas"
	exe, _ := os.Executable()
	cwd, _ := os.Getwd()
	args := strings.Join(os.Args[1:], " ")

	verbPtr, _ := syscall.UTF16PtrFromString(verb)
	exePtr, _ := syscall.UTF16PtrFromString(exe)
	cwdPtr, _ := syscall.UTF16PtrFromString(cwd)
	argPtr, _ := syscall.UTF16PtrFromString(args)

	var showCmd int32 = 1 //SW_NORMAL

	err := windows.ShellExecute(0, verbPtr, exePtr, argPtr, cwdPtr, showCmd)
	if err != nil {
		fmt.Println(err)
	}
}

//goland:noinspection GrazieInspection,GoUnhandledErrorResult
func setCTime(path string, ctime time.Time) error {
	//setCTime will set the create time on a file. On Windows, this requires
	//calling SetFileTime and explicitly including the create time.
	ctimespec := syscall.NsecToTimespec(ctime.UnixNano())
	pathp, e := syscall.UTF16PtrFromString(path)
	if e != nil {
		return e
	}
	h, e := syscall.CreateFile(pathp,
		syscall.FILE_WRITE_ATTRIBUTES, syscall.FILE_SHARE_WRITE, nil,
		syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if e != nil {
		return e
	}
	defer syscall.Close(h)
	c := syscall.NsecToFiletime(syscall.TimespecToNsec(ctimespec))
	return syscall.SetFileTime(h, &c, nil, nil)
}

func svcState(s *mgr.Service) svc.State {
	status, err := s.Query()
	if err != nil {
		panic(fmt.Sprintf("Query(%s) failed: %s", s.Name, err))
	}
	return status.State
}

func svcWaitState(s *mgr.Service, want svc.State) {
	for i := 0; ; i++ {
		have := svcState(s)
		if have == want {
			return
		}
		if i > 10 {
			panic(fmt.Sprintf("%s state is=%d, waiting timeout", s.Name, have))
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func svcStart(name string) {
	m, err := mgr.Connect()
	if err != nil {
		panic("SCM connection failed")
	}

	//goland:noinspection ALL
	defer m.Disconnect()

	s, err := m.OpenService(name)
	if err != nil {
		//println(fmt.Sprintf("Invalid service %s", name))
		return
	} else {
		println(fmt.Sprintf("Starting service %s", name))
	}

	//goland:noinspection ALL
	defer s.Close()

	if svcState(s) == svc.Stopped {
		err = s.Start()
		if err != nil {
			panic(fmt.Sprintf("Control(%s) failed: %s", name, err))
		}
		svcWaitState(s, svc.Running)
	}

	err = m.Disconnect()

}

func svcStop(name string) {
	m, err := mgr.Connect()
	if err != nil {
		panic("SCM connection failed")

	}

	//goland:noinspection ALL
	defer m.Disconnect()

	s, err := m.OpenService(name)
	if err != nil {
		//println(fmt.Sprintf("Invalid service %s", name))
		return
	} else {
		println(fmt.Sprintf("Stopping service %s", name))
	}

	//goland:noinspection ALL
	defer s.Close()

	if svcState(s) == svc.Running {
		_, err = s.Control(svc.Stop)
		if err != nil {
			panic(fmt.Sprintf("Control(%s) failed: %s", name, err))
		}
		svcWaitState(s, svc.Stopped)
	}

	err = m.Disconnect()

}

func taskStart(filename string) {
	println(fmt.Sprintf("Starting task %s", filename))
	c := exec.Command(filename)
	_ = c.Start()
	return
}

func taskRunning(name string) bool {
	pid, err := processID(name)
	if (pid != 0) && (err == nil) {
		return true
	} else {
		return false
	}
}

func taskStop(name string) {
	if taskRunning(name) {
		println(fmt.Sprintf("Stopping task %s", name))
		c := exec.Command("taskkill.exe", "/F", "/IM", name)
		_ = c.Run()
	}
	return
}

func vmwBackup(v *VMwareInfo) {
	currentFolder, _ := os.Getwd()
	backupFolder := filepath.Join(currentFolder, "backup", v.ProductVersion)
	backupFolder64 := filepath.Join(backupFolder, "x64")
	err := os.MkdirAll(backupFolder64, os.ModePerm)
	if err != nil {
		panic(err)
	}
	_, err = copyFile(v.PathVMwareBase, filepath.Join(backupFolder, v.VMwareBase))
	if err != nil {
		panic(err)
	}
	_, err = copyFile(v.PathVMXDefault, filepath.Join(backupFolder64, v.VMXDefault))
	if err != nil {
		panic(err)
	}
	_, err = copyFile(v.PathVMXDebug, filepath.Join(backupFolder64, v.VMXDebug))
	if err != nil {
		panic(err)
	}
	_, err = copyFile(v.PathVMXStats, filepath.Join(backupFolder64, v.VMXStats))
	if err != nil {
		panic(err)
	}
	_, err = copyFile(v.PathEFI32ROM, filepath.Join(backupFolder64, v.EFI32ROM))
	if err != nil {
		panic(err)
	}
	_, err = copyFile(v.PathEFI64ROM, filepath.Join(backupFolder64, v.EFI64ROM))
	if err != nil {
		panic(err)
	}
}

func vmwRestore(v *VMwareInfo) {
	currentFolder, _ := os.Getwd()
	backupFolder := filepath.Join(currentFolder, "backup", v.ProductVersion)
	backupFolder64 := filepath.Join(backupFolder, "x64")
	err := delFile(filepath.Join(backupFolder, v.VMwareBase), v.PathVMwareBase)
	if err != nil {
		panic(err)
	}
	err = delFile(filepath.Join(backupFolder64, v.VMXDefault), v.PathVMXDefault)
	if err != nil {
		panic(err)
	}
	err = delFile(filepath.Join(backupFolder64, v.VMXDebug), v.PathVMXDebug)
	if err != nil {
		panic(err)
	}
	err = delFile(filepath.Join(backupFolder64, v.VMXStats), v.PathVMXStats)
	if err != nil {
		panic(err)
	}
	err = delFile(filepath.Join(backupFolder64, v.EFI32ROM), v.PathEFI32ROM)
	if err != nil {
		panic(err)
	}
	err = delFile(filepath.Join(backupFolder64, v.EFI64ROM), v.PathEFI64ROM)
	if err != nil {
		panic(err)
	}

	err = os.RemoveAll(backupFolder)
}

func vmwInfo() *VMwareInfo {
	v := &VMwareInfo{}

	// Store known service names
	v.AuthD = "VMAuthdService"
	v.HostD = "VMwareHostd"
	v.USBD = "VMUSBArbService"

	// Access registry for version, build and installation path
	var access uint32
	access = registry.QUERY_VALUE
	if runtime.GOARCH == "amd64" {
		access = access | registry.WOW64_32KEY
	}
	regKey, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\VMware, Inc.\VMware Player`, access)
	if err != nil {
		panic("Failed to open registry")
	}
	//goland:noinspection GoUnhandledErrorResult
	defer regKey.Close()

	v.ProductVersion, _, err = regKey.GetStringValue("ProductVersion")
	if err != nil {
		panic("Failed to locate registry key ProductVersion")
	}

	v.BuildNumber, _, err = regKey.GetStringValue("BuildNumber")
	if err != nil {
		panic("Failed to locate registry key BuildNumber")
	}

	v.InstallDir, _, err = regKey.GetStringValue("InstallPath")
	if err != nil {
		panic("Failed to locate registry key InstallPath")
	}

	// Construct needed filenames from reg settings
	v.InstallDir64 = filepath.Join(v.InstallDir, "x64")
	v.Player = "vmplayer.exe"
	v.Workstation = "vmware.exe"
	v.KVM = "vmware-kvm.exe"
	v.REST = "vmrest.exe"
	v.Tray = "vmware-tray.exe"
	v.VMXDefault = "vmware-vmx.exe"
	v.VMXDebug = "vmware-vmx-debug.exe"
	v.VMXStats = "vmware-vmx-stats.exe"
	v.VMwareBase = "vmwarebase.dll"
	v.EFI32ROM = "EFI32.ROM"
	v.EFI64ROM = "EFI64.ROM"
	v.PathVMXDefault = filepath.Join(v.InstallDir64, "vmware-vmx.exe")
	v.PathVMXDebug = filepath.Join(v.InstallDir64, "vmware-vmx-debug.exe")
	v.PathVMXStats = filepath.Join(v.InstallDir64, "vmware-vmx-stats.exe")
	v.PathVMwareBase = filepath.Join(v.InstallDir, "vmwarebase.dll")
	v.PathEFI32ROM = filepath.Join(v.InstallDir64, "EFI32.ROM")
	v.PathEFI64ROM = filepath.Join(v.InstallDir64, "EFI64.ROM")

	return v
}

func vmwRunning(v *VMwareInfo) bool {
	if taskRunning(v.Workstation) {
		println("VMware Workstation is running")
		return true
	}
	if taskRunning(v.Player) {
		println("VMware Player is running")
		return true
	}
	if taskRunning(v.KVM) {
		println("VMware KVM is running")
		return true
	}
	if taskRunning(v.REST) {
		println("VMware REST API is running")
		return true
	}
	if taskRunning(v.VMXDefault) {
		println("VMware VM (vmware-vmx) is running")
		return true
	}
	if taskRunning(v.VMXDebug) {
		println("VMware VM (vmware-vmx-debug) is running")
		return true
	}
	if taskRunning(v.VMXStats) {
		println("VMware VM (vmware-vmx-stats) is running")
		return true
	}
	return false
}

func main() {
	// Titles
	println(fmt.Sprintf("Unlocker %s for VMware Workstation/Player", vmwpatch.VERSION))
	println("============================================")
	println(fmt.Sprintf("%s \n", vmwpatch.COPYRIGHT))

	// Simple arg parser
	if len(os.Args) < 2 {
		printHelp()
		return
	}
	var install bool
	switch os.Args[1] {
	case "install":
		install = true
	case "uninstall":
		install = false
	default:
		printHelp()
		return
	}

	// Check admin rights
	// https://gist.github.com/jerblack/d0eb182cc5a1c1d92d92a4c4fcc416c6
	if !amAdmin() {
		runElevated()
	}

	// Get VMware product details from registry and file system
	v := vmwInfo()
	println(fmt.Sprintf("VMware is installed at: %s", v.InstallDir))
	println(fmt.Sprintf("Patching VMware version %s", v.ProductVersion))

	// Check no VMs running
	if vmwRunning(v) {
		println("Aborting patching!")
		return
	}

	// Stop all VMW services and tasks
	println("\nStopping VMware services and tasks...")
	svcStop(v.AuthD)
	svcStop(v.HostD)
	svcStop(v.USBD)
	taskStop(v.Tray)

	if install {
		// Backup files
		println("\nBacking up files...")
		vmwBackup(v)

		// Patch files
		println("\nPatching...")
		vmwpatch.PatchSMC(v.PathVMXDefault)
		println()
		vmwpatch.PatchSMC(v.PathVMXDebug)
		println()
		vmwpatch.PatchSMC(v.PathVMXStats)
		println()
		vmwpatch.PatchGOS(v.PathVMwareBase)

		// Copy tools ISOs
		println("\nCopying VMware Tools...")
		_, _ = copyFile("./tools/darwinPre15.iso", filepath.Join(v.InstallDir, "darwinPre15.iso"))
		_, _ = copyFile("./tools/darwin.iso", filepath.Join(v.InstallDir, "darwin.iso"))

	} else {
		// Restore files
		println("\nRestoring files...")
		vmwRestore(v)

		// Removing tools ISOs
		println("\nRemoving VMware Tools...")
		_ = os.Remove(filepath.Join(v.InstallDir, "darwinPre15.iso"))
		_ = os.Remove(filepath.Join(v.InstallDir, "darwin.iso"))

	}

	// Start all VMW services and tasks
	println("\nStarting VMware services and tasks...")
	svcStart(v.AuthD)
	svcStart(v.HostD)
	svcStart(v.USBD)
	taskStart(filepath.Join(v.InstallDir, v.Tray))

	println("\nFinished!")
	return
}
