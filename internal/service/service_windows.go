package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// BinaryPath is %ProgramFiles%\restash\restash-agent.exe.
func BinaryPath() (string, error) {
	pf := os.Getenv("ProgramFiles")
	if pf == "" {
		return "", errors.New("%ProgramFiles% isn't set")
	}
	return filepath.Join(pf, "restash", Name+".exe"), nil
}

// LogPath is where the service writes its log.
func LogPath() string {
	return filepath.Join(os.Getenv("ProgramData"), "restash", "agent.log")
}

// LogHint tells where the service's log is.
func LogHint() string { return LogPath() }

func connect() (*mgr.Mgr, error) {
	m, err := mgr.Connect()
	if err != nil {
		return nil, fmt.Errorf("can't reach the Windows service manager (run as administrator): %w", err)
	}
	return m, nil
}

func stopIfRunning() error {
	m, err := connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(Name)
	if err != nil {
		return nil // not installed
	}
	defer s.Close()
	return stop(s)
}

func stop(s *mgr.Service) error {
	st, err := s.Query()
	if err != nil || st.State == svc.Stopped {
		return nil
	}
	if _, err := s.Control(svc.Stop); err != nil {
		return fmt.Errorf("stopping the service: %w", err)
	}
	for i := 0; i < 60; i++ {
		time.Sleep(500 * time.Millisecond)
		if st, err := s.Query(); err != nil || st.State == svc.Stopped {
			return nil
		}
	}
	return errors.New("the service didn't stop within 30 seconds")
}

func install(exe string) error {
	m, err := connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	conf := mgr.Config{
		DisplayName:      "restash",
		Description:      "Backs up this PC's folders to restash.",
		StartType:        mgr.StartAutomatic,
		DelayedAutoStart: true,
	}
	s, err := m.OpenService(Name)
	if err == nil {
		c, err := s.Config()
		if err == nil {
			c.BinaryPathName = windows.EscapeArg(exe) + " " + joinArgs(Args)
			c.DisplayName, c.Description, c.StartType, c.DelayedAutoStart = conf.DisplayName, conf.Description, conf.StartType, conf.DelayedAutoStart
			err = s.UpdateConfig(c)
		}
		if err != nil {
			s.Close()
			return fmt.Errorf("updating the service: %w", err)
		}
	} else {
		s, err = m.CreateService(Name, exe, conf, Args...)
		if err != nil {
			return fmt.Errorf("creating the service: %w", err)
		}
	}
	defer s.Close()
	// Restart when it stops on its own (crashes, and after updating itself).
	actions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 2 * time.Minute},
	}
	if err := s.SetRecoveryActions(actions, 24*60*60); err != nil {
		return fmt.Errorf("setting restart rules: %w", err)
	}
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return fmt.Errorf("setting restart rules: %w", err)
	}
	if err := s.Start(); err != nil {
		return fmt.Errorf("starting the service: %w", err)
	}
	return nil
}

func joinArgs(args []string) string {
	out := ""
	for i, a := range args {
		if i > 0 {
			out += " "
		}
		out += windows.EscapeArg(a)
	}
	return out
}

func uninstall() error {
	m, err := connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(Name)
	if err != nil {
		return nil
	}
	defer s.Close()
	if err := stop(s); err != nil {
		return err
	}
	return s.Delete()
}

// Running says whether the service is running.
func Running() bool {
	m, err := mgr.Connect()
	if err != nil {
		return false
	}
	defer m.Disconnect()
	s, err := m.OpenService(Name)
	if err != nil {
		return false
	}
	defer s.Close()
	st, err := s.Query()
	return err == nil && st.State == svc.Running
}
