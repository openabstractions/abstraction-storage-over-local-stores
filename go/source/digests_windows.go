package source

import (
	"runtime"

	"golang.org/x/sys/windows"
)

var setThreadPriority = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetThreadPriority")

const (
	threadModeBackgroundBegin = 0x00010000
	threadModeBackgroundEnd   = 0x00020000
)

// backgroundIO puts the calling goroutine's thread into Windows' background
// processing mode, which lowers its disk and memory priority, so an
// on-demand hash (LS-13) yields to the person's own work. The returned
// function leaves the mode and releases the thread.
func backgroundIO() func() {
	runtime.LockOSThread()
	thread, err := windows.GetCurrentThread()
	if err != nil {
		runtime.UnlockOSThread()
		return func() {}
	}
	if r, _, _ := setThreadPriority.Call(uintptr(thread), threadModeBackgroundBegin); r == 0 {
		runtime.UnlockOSThread()
		return func() {}
	}
	return func() {
		//unchecked: leaving background mode on a thread about to be released; a failure leaves only this worker thread at low priority
		setThreadPriority.Call(uintptr(thread), threadModeBackgroundEnd)
		runtime.UnlockOSThread()
	}
}
