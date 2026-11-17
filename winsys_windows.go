//go:build windows

package main

import (
	"os"
	"os/exec"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	appName = "ClaudeUsageMeter"
	runKey  = `Software\Microsoft\Windows\CurrentVersion\Run`
)

var instanceMutex windows.Handle

// singleInstance returns false if another copy of the meter is already running.
func singleInstance() bool {
	name, _ := windows.UTF16PtrFromString(`Local\` + appName)
	h, err := windows.CreateMutex(nil, false, name)
	if err == windows.ERROR_ALREADY_EXISTS {
		if h != 0 {
			windows.CloseHandle(h)
		}
		return false
	}
	instanceMutex = h
	return true
}

// autostartEnabled reports whether the meter starts with Windows (current user only).
func autostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(appName)
	return err == nil
}

// setAutostart adds or removes the per-user Run entry. No admin rights needed.
func setAutostart(on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		err := k.DeleteValue(appName)
		if err == registry.ErrNotExist {
			return nil
		}
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return k.SetStringValue(appName, `"`+exe+`"`)
}

// open opens a file or folder with its default Windows handler.
func open(path string) { _ = exec.Command("explorer.exe", path).Start() }

// openText opens a text file in Notepad.
func openText(path string) { _ = exec.Command("notepad.exe", path).Start() }
