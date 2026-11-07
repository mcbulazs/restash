//go:build !linux && !darwin && !windows

package service

import "errors"

var errUnsupported = errors.New("installing as a service isn't supported on this system; keep `restash-agent run` going another way")

func BinaryPath() (string, error) { return "", errUnsupported }
func stopIfRunning() error        { return nil }
func install(string) error        { return errUnsupported }
func uninstall() error            { return errUnsupported }
func Running() bool               { return false }
func LogHint() string             { return "" }
