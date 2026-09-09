// Package agent is the client: it supervises userspace tailscaled, runs rclone per circle, heartbeats to the hub
// and keeps the only user-facing surface a folder (C-3).
package agent

import (
	"os"
	"path/filepath"
	"runtime"
)

const (
	ServiceName = "Quietport"
	SyncDirName = "QPSync"
	VersionsDir = ".qp-versions"
)

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

// AppDir: FR-12.
func AppDir() string {
	switch runtime.GOOS {
	case "windows":
		if l := os.Getenv("LOCALAPPDATA"); l != "" {
			return filepath.Join(l, "Quietport")
		}
		return filepath.Join(home(), "AppData", "Local", "Quietport")
	case "darwin":
		return filepath.Join(home(), "Library", "Application Support", "Quietport")
	default:
		if x := os.Getenv("XDG_DATA_HOME"); x != "" {
			return filepath.Join(x, "quietport")
		}
		return filepath.Join(home(), ".local", "share", "quietport")
	}
}

func SyncRoot() string        { return filepath.Join(home(), SyncDirName) }
func ConfigPath() string      { return filepath.Join(AppDir(), "config.json") }
func StatePath() string       { return filepath.Join(AppDir(), "state.json") }
func LogDir() string          { return filepath.Join(AppDir(), "logs") }
func TSDir() string           { return filepath.Join(AppDir(), "ts") }
func BisyncDir() string       { return filepath.Join(AppDir(), "bisync") }
func UpdateStatePath() string { return filepath.Join(AppDir(), "update.json") }

func exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func AgentBin() string     { return filepath.Join(AppDir(), exe("qpsync-agent")) }
func RcloneBin() string    { return filepath.Join(AppDir(), exe("rclone")) }
func TailscaledBin() string { return filepath.Join(AppDir(), exe("tailscaled")) }
func TailscaleBin() string { return filepath.Join(AppDir(), exe("tailscale")) }

// TSSocket: macOS unix sockets are limited to 104 bytes of path, so keep it short; Windows uses a private named pipe
// (the default pipe name is admin-only, which is exactly what we must avoid: C-2).
func TSSocket() string {
	if runtime.GOOS == "windows" {
		return `\\.\pipe\quietport-tailscaled`
	}
	return filepath.Join(TSDir(), "ts.sock")
}

// MaxPath for FR-40.
func MaxPath() int {
	if runtime.GOOS == "windows" {
		return 259
	}
	return 1023
}
