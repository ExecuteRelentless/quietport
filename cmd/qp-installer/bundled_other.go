//go:build !darwin && !windows

package main

func installBundled(app string) error { return errNoBundled }
