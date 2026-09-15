//go:build !windows && !linux

package main

func installBundled(app string) error { return errNoBundled }
