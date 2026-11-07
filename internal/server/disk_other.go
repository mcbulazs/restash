//go:build !unix

package server

func diskUsage(string) (int64, int64) { return 0, 0 }
