//go:build !darwin && !windows && !linux

package main

func installBundled(app string) error { return errNoBundled }
