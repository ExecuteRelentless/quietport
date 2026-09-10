package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"quietport.app/quietport/internal/cred"
	"quietport.app/quietport/internal/cryptobox"
	"quietport.app/quietport/internal/model"
)

// Install runs the personalised installer (FR-13..18). It prints exactly one line on success.
// Every failure returns a single plain sentence; the caller appends the support contact.
func Install(ctx context.Context, code, payloadPath string) (err error) {
	raw, err := os.ReadFile(payloadPath)
	if err != nil {
		return errors.New("the invitation file could not be read.")
	}
	var p model.InvitePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return errors.New("the invitation file is damaged.")
	}
	var keys []model.CircleKey
	if p.NewCircleID != 0 {
		// self-serve start: this computer creates the folder's key (FR-49 moved to the founding member's device)
		keys = append(keys, model.CircleKey{Slug: p.NewCircleSlug, Generation: 1, Password: cryptobox.NewCircleSecret(), Salt: cryptobox.NewCircleSecret()})
	} else if err := cryptobox.OpenWithCode(code, p.SealedKeys, &keys); err != nil {
		return errors.New("the invitation code does not match this installer.")
	}
	store, err := OpenStore()
	if err != nil {
		return errors.New("the secure storage on this computer could not be opened.")
	}
	dk, err := cryptobox.NewDeviceKeys()
	if err != nil {
		return errors.New("a device key could not be generated.")
	}
	port := FreePort()
	if old := store.Config(); old.SocksPort != 0 {
		port = old.SocksPort
		stopRunningAgent()
	}
	_ = os.MkdirAll(SyncRoot(), 0o755)
	for _, name := range p.Circles {
		_ = os.MkdirAll(CircleDir(name), 0o755) // FR-15
	}
	sealedKeys, _ := store.Seal(dk)
	sealedPak, _ := store.Seal(p.PreAuthKey)
	err = store.Update(func(c *Config) {
		c.Version = 1
		c.AgentVersion = Version
		c.HubAPI, c.LoginServer, c.SupportContact, c.OperatorName = p.HubAPI, p.LoginServer, p.SupportContact, p.OperatorName
		c.SocksPort = port
		if c.UIPort == 0 {
			c.UIPort = FreePort()
		}
		if c.UIToken == "" {
			c.UIToken = cryptobox.NewToken()
		}
		c.SyncInterval = model.DefaultSyncSeconds
		c.DeviceKeysSealed = sealedKeys
		c.PreAuthSealed = sealedPak
		c.Circles = nil
		for _, k := range keys {
			s, _ := store.Seal(k)
			c.Circles = append(c.Circles, CircleState{CircleConfig: model.CircleConfig{Slug: k.Slug, Generation: k.Generation}, KeySealed: s, KeyGen: k.Generation, Resync: true})
		}
	})
	if err != nil {
		return errors.New("the configuration could not be saved.")
	}
	// FR-16: the plaintext payload (pre-auth key + sealed keys) leaves the disk now
	_ = os.Remove(payloadPath)
	_ = os.WriteFile(payloadPath, []byte{}, 0o600)
	_ = os.Remove(payloadPath)

	// bring the mesh up in the foreground to verify connectivity before reporting success (FR-18)
	ts := NewTS(port)
	tctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go ts.Run(tctx, func(string, ...any) {})
	time.Sleep(1500 * time.Millisecond)
	if err := ts.Up(tctx, p.LoginServer, p.PreAuthKey); err != nil {
		return errors.New("this computer could not reach the network.")
	}
	st, err := ts.WaitRunning(tctx, 60*time.Second)
	if err != nil {
		return errors.New("this computer could not join the network.")
	}
	hub, err := NewHubClient(p.HubAPI, "", ts.ProxyURL())
	if err != nil {
		return errors.New("the connection could not be set up.")
	}
	host, _ := os.Hostname()
	var enr model.EnrolResponse
	for i := 0; i < 10; i++ {
		enr, err = hub.Enrol(tctx, model.EnrolRequest{InviteCode: code, Hostname: host, OS: runtime.GOOS, Arch: runtime.GOARCH, AgentVersion: Version, TailnetIP: st.TailnetIP, PubKey: dk.Public})
		if err == nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		return errors.New("the network accepted this computer but the hub did not.")
	}
	tokSealed, _ := store.Seal(enr.DeviceToken)
	_ = store.Update(func(c *Config) {
		c.DeviceID = enr.DeviceID
		c.DeviceTokenSealed = tokSealed
		c.PreAuthSealed = ""
	})
	// fold the config bundle in (circle display names, S3 creds) using the same code path the agent uses
	a := &Agent{store: store, ts: ts, logger: newLogger(), st: LoadState(), hub: hub}
	a.applyBundle(tctx, enr.Config)
	for _, c := range store.Config().Circles {
		_ = os.MkdirAll(CircleDir(c.DisplayName), 0o755)
	}
	cancel() // the background agent owns tailscaled from here
	time.Sleep(500 * time.Millisecond)

	if err := registerStartup(); err != nil { // FR-14
		return errors.New("the startup entry could not be created.")
	}
	pinFolder(SyncRoot()) // FR-15, best effort
	writeHelpers()
	if err := startAgent(); err != nil {
		return errors.New("the background service could not be started.")
	}
	return nil
}

func writeHelpers() {
	if runtime.GOOS == "windows" {
		_ = os.WriteFile(filepath.Join(AppDir(), "qp.cmd"), []byte("@echo off\r\n\"%~dp0qpsync-agent.exe\" %*\r\n"), 0o755)
		return
	}
	_ = os.WriteFile(filepath.Join(AppDir(), "qp"), []byte("#!/bin/sh\nexec \""+AgentBin()+"\" \"$@\"\n"), 0o755)
}

// Uninstall: FR-19. Logs the device out of the mesh first so the hub sees it go, then removes everything.
func Uninstall(removeFolder bool) error {
	if st, err := OpenStore(); err == nil {
		if c := st.Config(); c.SocksPort != 0 {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			NewTS(c.SocksPort).Logout(ctx)
			cancel()
		}
	}
	stopRunningAgent()
	_ = unregisterStartup()
	unpinFolder(SyncRoot())
	removeShareLink()
	_ = cred.Destroy(AppDir(), ServiceName)
	if removeFolder {
		_ = os.RemoveAll(SyncRoot())
	}
	// remove everything but the running binary's directory contents are freed on exit; on Windows the exe cannot delete itself
	entries, _ := os.ReadDir(AppDir())
	for _, e := range entries {
		p := filepath.Join(AppDir(), e.Name())
		if runtime.GOOS == "windows" && strings.EqualFold(e.Name(), "qpsync-agent.exe") {
			continue
		}
		_ = os.RemoveAll(p)
	}
	if runtime.GOOS == "windows" {
		// schedule deletion of the directory after we exit
		cmd := exec.Command("cmd", "/c", "ping 127.0.0.1 -n 3 >nul & rmdir /s /q \""+AppDir()+"\"")
		hideWindow(cmd)
		_ = cmd.Start()
	} else {
		_ = os.RemoveAll(AppDir())
	}
	return nil
}

// --- startup entries ---

const launchLabel = "app.quietport.agent"

func plistPath() string {
	return filepath.Join(home(), "Library", "LaunchAgents", launchLabel+".plist")
}

func registerStartup() error {
	switch runtime.GOOS {
	case "darwin":
		_ = os.MkdirAll(filepath.Dir(plistPath()), 0o755)
		pl := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s</string><string>run</string></array>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><true/>
<key>ProcessType</key><string>Background</string>
<key>StandardOutPath</key><string>%s</string>
<key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, launchLabel, AgentBin(), filepath.Join(LogDir(), "launchd.out"), filepath.Join(LogDir(), "launchd.err"))
		return os.WriteFile(plistPath(), []byte(pl), 0o644)
	case "windows":
		return registerTask()
	default:
		dir := filepath.Join(home(), ".config", "systemd", "user")
		_ = os.MkdirAll(dir, 0o755)
		unit := fmt.Sprintf("[Unit]\nDescription=Quietport\n[Service]\nExecStart=%s run\nRestart=always\nRestartSec=5\n[Install]\nWantedBy=default.target\n", AgentBin())
		if err := os.WriteFile(filepath.Join(dir, "quietport.service"), []byte(unit), 0o644); err != nil {
			return err
		}
		return exec.Command("systemctl", "--user", "enable", "quietport.service").Run()
	}
}

func unregisterStartup() error {
	switch runtime.GOOS {
	case "darwin":
		_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), launchLabel)).Run()
		return os.Remove(plistPath())
	case "windows":
		_ = exec.Command("reg", "delete", `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, "/v", "Quietport", "/f").Run()
		return exec.Command("schtasks", "/Delete", "/TN", "Quietport", "/F").Run()
	default:
		_ = exec.Command("systemctl", "--user", "disable", "--now", "quietport.service").Run()
		return os.Remove(filepath.Join(home(), ".config", "systemd", "user", "quietport.service"))
	}
}

func startAgent() error {
	switch runtime.GOOS {
	case "darwin":
		_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), launchLabel)).Run()
		return exec.Command("launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), plistPath()).Run()
	case "windows":
		cmd := exec.Command("schtasks", "/Run", "/TN", "Quietport")
		hideWindow(cmd)
		if err := cmd.Run(); err == nil {
			return nil
		}
		c := exec.Command(AgentBin(), "run")
		hideWindow(c)
		return c.Start()
	default:
		return exec.Command("systemctl", "--user", "restart", "quietport.service").Run()
	}
}

func stopRunningAgent() {
	switch runtime.GOOS {
	case "darwin":
		_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), launchLabel)).Run()
	case "windows":
		_ = exec.Command("schtasks", "/End", "/TN", "Quietport").Run()
		_ = exec.Command("taskkill", "/IM", "qpsync-agent.exe", "/F").Run()
		_ = exec.Command("taskkill", "/IM", "tailscaled.exe", "/F").Run()
	default:
		_ = exec.Command("systemctl", "--user", "stop", "quietport.service").Run()
	}
	time.Sleep(time.Second)
}

// registerTask: a per-user Scheduled Task at logon with restart-on-failure (FR-14). No admin needed for the current user.
func registerTask() error {
	user := os.Getenv("USERNAME")
	if d := os.Getenv("USERDOMAIN"); d != "" {
		user = d + `\` + user
	}
	xml := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Description>Quietport shared folders</Description></RegistrationInfo>
  <Triggers><LogonTrigger><Enabled>true</Enabled><UserId>%s</UserId></LogonTrigger></Triggers>
  <Principals><Principal id="Author"><UserId>%s</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings><StopOnIdleEnd>false</StopOnIdleEnd><RestartOnIdle>false</RestartOnIdle></IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>true</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>7</Priority>
    <RestartOnFailure><Interval>PT1M</Interval><Count>999</Count></RestartOnFailure>
  </Settings>
  <Actions Context="Author"><Exec><Command>%s</Command><Arguments>run</Arguments></Exec></Actions>
</Task>`, user, user, AgentBin())
	f := filepath.Join(AppDir(), "task.xml")
	// schtasks wants UTF-16LE with BOM for the XML declaration above
	if err := os.WriteFile(f, utf16le(xml), 0o600); err != nil {
		return err
	}
	cmd := exec.Command("schtasks", "/Create", "/TN", "Quietport", "/XML", f, "/F")
	hideWindow(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		// fallback: HKCU Run key (still per-user, no admin)
		reg := exec.Command("reg", "add", `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, "/v", "Quietport", "/t", "REG_SZ", "/d", `"`+AgentBin()+`" run`, "/f")
		hideWindow(reg)
		if rerr := reg.Run(); rerr != nil {
			return fmt.Errorf("schtasks: %v: %s; reg: %v", err, out, rerr)
		}
	}
	return nil
}

func utf16le(s string) []byte {
	out := []byte{0xFF, 0xFE}
	for _, r := range s {
		if r > 0xFFFF {
			r = '?'
		}
		out = append(out, byte(r), byte(r>>8))
	}
	return out
}

// --- folder pinning (FR-15) ---

func pinFolder(p string) {
	switch runtime.GOOS {
	case "darwin":
		_ = exec.Command(filepath.Join(AppDir(), "qp-sidebar"), "add", p).Run()
	case "windows":
		ps := `$o = New-Object -ComObject shell.application; $o.Namespace('` + p + `').Self.InvokeVerb('pintohome')`
		cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", ps)
		hideWindow(cmd)
		_ = cmd.Run()
	}
}

func unpinFolder(p string) {
	switch runtime.GOOS {
	case "darwin":
		_ = exec.Command(filepath.Join(AppDir(), "qp-sidebar"), "remove", p).Run()
	case "windows":
		ps := `$o = New-Object -ComObject shell.application; $o.Namespace('` + p + `').Self.InvokeVerb('unpinfromhome')`
		cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", ps)
		hideWindow(cmd)
		_ = cmd.Run()
	}
}

// UninstallSelf is the version run by the agent itself (from the "Remove Quietport" page). It cannot call
// stopRunningAgent (that would kill this process before the work is done), so it removes everything first and
// hands its own job to launchd/systemd at the very end.
func UninstallSelf(ts *TS, removeFolder bool) {
	if ts != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		ts.Logout(ctx)
		cancel()
	}
	_ = unregisterStartupFiles()
	unpinFolder(SyncRoot())
	removeShareLink()
	_ = cred.Destroy(AppDir(), ServiceName)
	if removeFolder {
		_ = os.RemoveAll(SyncRoot())
	}
	if runtime.GOOS == "windows" {
		_ = exec.Command("taskkill", "/IM", "tailscaled.exe", "/F").Run()
		cmd := exec.Command("cmd", "/c", "ping 127.0.0.1 -n 3 >nul & rmdir /s /q \""+AppDir()+"\"")
		hideWindow(cmd)
		_ = cmd.Start()
		os.Exit(0)
	}
	entries, _ := os.ReadDir(AppDir())
	for _, e := range entries {
		_ = os.RemoveAll(filepath.Join(AppDir(), e.Name()))
	}
	_ = os.RemoveAll(AppDir())
	switch runtime.GOOS {
	case "darwin":
		// bootout kills this process; everything is already gone
		_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), launchLabel)).Run()
	default:
		_ = exec.Command("systemctl", "--user", "stop", "quietport.service").Start()
	}
	os.Exit(0)
}

// unregisterStartupFiles removes the startup entry without stopping the running job.
func unregisterStartupFiles() error {
	switch runtime.GOOS {
	case "darwin":
		return os.Remove(plistPath())
	case "windows":
		_ = exec.Command("reg", "delete", `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, "/v", "Quietport", "/f").Run()
		return exec.Command("schtasks", "/Delete", "/TN", "Quietport", "/F").Run()
	default:
		_ = exec.Command("systemctl", "--user", "disable", "quietport.service").Run()
		return os.Remove(filepath.Join(home(), ".config", "systemd", "user", "quietport.service"))
	}
}

// Installed reports whether this account has a working Quietport install.
func Installed() bool {
	b, err := os.ReadFile(ConfigPath())
	if err != nil {
		return false
	}
	return strings.Contains(string(b), `"device_id"`) && !strings.Contains(string(b), `"device_id": 0,`)
}
