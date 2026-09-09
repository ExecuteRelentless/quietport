//go:build !darwin

package main

// Windows keeps the origin in a Zone.Identifier stream; the exe name carries the code and the host is compiled in.
func originHost(exe string) string { return "" }
