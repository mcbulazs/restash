//go:build !windows

package main

import (
	"context"
	"errors"
)

// elevated is always true here: the service is per user and needs no
// administrator.
func elevated() bool { return true }

func relaunchElevated([]string) error { return errors.New("not needed on this system") }

func pause() {}

func runAsService(func(ctx context.Context) error) (bool, error) { return false, nil }
