package main

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"

	"github.com/mcbulazs/restash/internal/service"
)

func elevated() bool { return windows.GetCurrentProcessToken().IsElevated() }

// relaunchElevated starts this program again as administrator (the UAC
// prompt), in a new console window.
func relaunchElevated(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = windows.EscapeArg(a)
	}
	cwd, _ := os.Getwd()
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	params, _ := windows.UTF16PtrFromString(strings.Join(quoted, " "))
	dir, _ := windows.UTF16PtrFromString(cwd)
	return windows.ShellExecute(0, verb, file, params, dir, windows.SW_NORMAL)
}

// ownConsole says whether this program has the console window to itself,
// i.e. it was opened by double-clicking rather than from a terminal.
func ownConsole() bool {
	ids := make([]uint32, 4)
	n, _, _ := getConsoleProcessList.Call(uintptr(unsafe.Pointer(&ids[0])), uintptr(len(ids)))
	return n == 1
}

var getConsoleProcessList = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleProcessList")

// pause keeps a double-clicked window open until Enter is pressed.
func pause() {
	if !ownConsole() {
		return
	}
	fmt.Fprint(os.Stderr, "\nPress Enter to close this window.")
	bufio.NewReader(os.Stdin).ReadString('\n')
}

// runAsService runs run under the Windows service manager when started by
// it. handled is false when started any other way.
func runAsService(run func(ctx context.Context) error) (handled bool, err error) {
	is, err := svc.IsWindowsService()
	if err != nil || !is {
		return false, nil
	}
	if f, err := os.OpenFile(service.LogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		trimLog(f)
		slog.SetDefault(slog.New(slog.NewTextHandler(f, nil)))
	}
	return true, svc.Run(service.Name, &handler{run: run})
}

// trimLog starts the log over once it passes 10 MB.
func trimLog(f *os.File) {
	if fi, err := f.Stat(); err == nil && fi.Size() > 10<<20 {
		f.Truncate(0)
	}
}

type handler struct {
	run func(ctx context.Context) error
}

func (h *handler) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.run(ctx) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				select {
				case <-done:
				case <-time.After(20 * time.Second):
				}
				return false, 0
			}
		case err := <-done:
			// Stopped on its own (after an update, or an error): a non-zero
			// exit makes Windows start it again.
			if err != nil {
				slog.Error("stopped", "err", err)
			}
			return false, 1
		}
	}
}
