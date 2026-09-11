//go:build windows

// pipeprobe reproduces the Windows install failure (docs: hub 2026-09-10 late entry).
// It reports the caller's token, tries to create tailscaled's named pipe with the stock
// security descriptor and with one that names no owner, then launches every
// tailscaled*.exe found in <bindir> exactly the way qpsync-agent does and reports whether
// the pipe ever answers and what tailscaled printed. Run once elevated (control) and once
// as a non-admin (--as-normal-user re-executes itself under a Safer "normal user" token).
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/tailscale/go-winio"
	"golang.org/x/sys/windows"
)

const (
	stockSDDL   = "O:BAG:BAD:PAI(A;OICI;GWGR;;;BU)(A;OICI;GWGR;;;SY)" // tailscale.com/safesocket/pipe_windows.go
	noOwnerSDDL = "D:PAI(A;OICI;GWGR;;;BU)(A;OICI;GWGR;;;SY)"
	pipeName    = `\\.\pipe\quietport-tailscaled` // internal/agent/paths.go TSSocket()
)

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "--as-normal-user" {
		if err := reexecNormalUser(args[1:]); err != nil {
			fmt.Println("PROBE: re-exec as normal user FAILED:", err)
			os.Exit(2)
		}
		return
	}
	if len(args) < 2 {
		fmt.Println("usage: pipeprobe [--as-normal-user] <bindir> <workdir>")
		os.Exit(1)
	}
	bindir, workdir := args[0], args[1]
	reportIdentity()
	tryListen("stock", stockSDDL)
	tryListen("no-owner", noOwnerSDDL)
	bins, _ := filepath.Glob(filepath.Join(bindir, "tailscaled*.exe"))
	sort.Strings(bins)
	for _, b := range bins {
		runTailscaled(b, filepath.Join(bindir, "tailscale.exe"), workdir)
	}
	fmt.Println("PROBE: done")
}

func reportIdentity() {
	fmt.Printf("PROBE: env USERPROFILE=%q LOCALAPPDATA=%q\n", os.Getenv("USERPROFILE"), os.Getenv("LOCALAPPDATA"))
	tok := windows.GetCurrentProcessToken()
	if u, err := tok.GetTokenUser(); err == nil {
		acct, dom, _, _ := u.User.Sid.LookupAccount("")
		fmt.Printf("PROBE: user=%s\\%s sid=%s elevated=%v\n", dom, acct, u.User.Sid, tok.IsElevated())
	}
	var n uint32
	windows.GetTokenInformation(tok, windows.TokenOwner, nil, 0, &n)
	buf := make([]byte, n)
	if err := windows.GetTokenInformation(tok, windows.TokenOwner, &buf[0], n, &n); err == nil {
		owner := (*windows.Tokenprimarygroup)(unsafe.Pointer(&buf[0])).PrimaryGroup // same layout: one *SID
		acct, dom, _, _ := owner.LookupAccount("")
		fmt.Printf("PROBE: token default owner=%s\\%s (%s)\n", dom, acct, owner)
	}
	if g, err := tok.GetTokenGroups(); err == nil {
		found := false
		for _, ga := range g.AllGroups() {
			if ga.Sid.String() == "S-1-5-32-544" {
				found = true
				fmt.Printf("PROBE: BUILTIN\\Administrators in token: attrs=0x%x enabled=%v owner=%v denyOnly=%v\n", ga.Attributes,
					ga.Attributes&windows.SE_GROUP_ENABLED != 0, ga.Attributes&windows.SE_GROUP_OWNER != 0, ga.Attributes&windows.SE_GROUP_USE_FOR_DENY_ONLY != 0)
			}
		}
		if !found {
			fmt.Println("PROBE: BUILTIN\\Administrators NOT in token")
		}
	}
}

func tryListen(label, sddl string) {
	l, err := winio.ListenPipe(`\\.\pipe\qp-probe-`+label, &winio.PipeConfig{SecurityDescriptor: sddl, InputBufferSize: 256 * 1024, OutputBufferSize: 256 * 1024})
	if err != nil {
		var e windows.Errno
		code := ""
		if errors.As(err, &e) {
			code = fmt.Sprintf(" (errno %d)", uint32(e))
		}
		fmt.Printf("PROBE: ListenPipe %-8s SDDL=%q FAIL%s: %v\n", label, sddl, code, err)
		return
	}
	l.Close()
	fmt.Printf("PROBE: ListenPipe %-8s SDDL=%q ok\n", label, sddl)
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func runTailscaled(daemon, cli, workdir string) {
	name := filepath.Base(daemon)
	state := filepath.Join(workdir, strings.TrimSuffix(name, ".exe"))
	_ = os.MkdirAll(state, 0o700)
	port := freePort()
	// identical to internal/agent/tailscale.go Args()
	cmd := exec.Command(daemon, "--tun=userspace-networking", "--socks5-server=127.0.0.1:"+strconv.Itoa(port),
		"--state="+state+`\tailscaled.state`, "--statedir="+state, "--socket="+pipeName, "--port=0", "--no-logs-no-support")
	out := &lockedBuf{}
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	start := time.Now()
	if err := cmd.Start(); err != nil {
		fmt.Printf("PROBE: %s: start FAILED: %v\n", name, err)
		return
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ready := false
	var exitErr error
	exited := false
loop:
	for time.Since(start) < 20*time.Second {
		select {
		case exitErr = <-done:
			exited = true
			break loop
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		c, err := winio.DialPipeContext(ctx, pipeName)
		cancel()
		if err == nil {
			c.Close()
			ready = true
			break loop
		}
		time.Sleep(100 * time.Millisecond)
	}
	switch {
	case ready:
		fmt.Printf("PROBE: %s: pipe answered after %s\n", name, time.Since(start).Round(10*time.Millisecond))
		st := exec.Command(cli, "--socket="+pipeName, "status")
		st.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
		o, err := st.CombinedOutput()
		fmt.Printf("PROBE: %s: `tailscale status` err=%v output: %s\n", name, err, strings.TrimSpace(string(o)))
	case exited:
		fmt.Printf("PROBE: %s: EXITED after %s: %v\n", name, time.Since(start).Round(10*time.Millisecond), exitErr)
	default:
		fmt.Printf("PROBE: %s: still running after 20s but the pipe never answered\n", name)
	}
	if !exited {
		_ = cmd.Process.Kill()
		<-done
	}
	fmt.Printf("PROBE: %s output begins\n%s\nPROBE: %s output ends\n", name, strings.TrimSpace(out.String()), name)
}

func freePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 41055
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// --- Safer: re-run this program with a "normal user" token (Administrators disabled, medium integrity) ---

var (
	advapi32                       = windows.NewLazySystemDLL("advapi32.dll")
	procSaferCreateLevel           = advapi32.NewProc("SaferCreateLevel")
	procSaferComputeTokenFromLevel = advapi32.NewProc("SaferComputeTokenFromLevel")
	procSaferCloseLevel            = advapi32.NewProc("SaferCloseLevel")
)

const (
	saferScopeIDUser       = 2
	saferLevelIDNormalUser = 0x20000
	saferLevelOpen         = 1
	mediumIntegritySID     = "S-1-16-8192"
)

func reexecNormalUser(args []string) error {
	var lvl uintptr
	r, _, e := procSaferCreateLevel.Call(saferScopeIDUser, saferLevelIDNormalUser, saferLevelOpen, uintptr(unsafe.Pointer(&lvl)), 0)
	if r == 0 {
		return fmt.Errorf("SaferCreateLevel: %v", e)
	}
	defer procSaferCloseLevel.Call(lvl)
	var tok windows.Token
	r, _, e = procSaferComputeTokenFromLevel.Call(lvl, 0, uintptr(unsafe.Pointer(&tok)), 0, 0)
	if r == 0 {
		return fmt.Errorf("SaferComputeTokenFromLevel: %v", e)
	}
	defer tok.Close()
	sid, err := windows.StringToSid(mediumIntegritySID)
	if err == nil {
		tml := windows.Tokenmandatorylabel{Label: windows.SIDAndAttributes{Sid: sid, Attributes: windows.SE_GROUP_INTEGRITY}}
		if err := windows.SetTokenInformation(tok, windows.TokenIntegrityLevel, (*byte)(unsafe.Pointer(&tml)), tml.Size()); err != nil {
			fmt.Println("PROBE: (medium integrity not applied:", err, ")")
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	parts := []string{syscall.EscapeArg(exe)}
	for _, a := range args {
		parts = append(parts, syscall.EscapeArg(a))
	}
	cmdline, err := windows.UTF16PtrFromString(strings.Join(parts, " "))
	if err != nil {
		return err
	}
	for _, h := range []windows.Handle{windows.Stdin, windows.Stdout, windows.Stderr} {
		_ = windows.SetHandleInformation(h, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT)
	}
	si := &windows.StartupInfo{StdInput: windows.Stdin, StdOutput: windows.Stdout, StdErr: windows.Stderr, Flags: windows.STARTF_USESTDHANDLES}
	si.Cb = uint32(unsafe.Sizeof(*si))
	pi := &windows.ProcessInformation{}
	if err := windows.CreateProcessAsUser(tok, nil, cmdline, nil, nil, true, 0, nil, nil, si, pi); err != nil {
		return fmt.Errorf("CreateProcessAsUser: %w", err)
	}
	defer windows.CloseHandle(pi.Thread)
	defer windows.CloseHandle(pi.Process)
	if _, err := windows.WaitForSingleObject(pi.Process, windows.INFINITE); err != nil {
		return err
	}
	var code uint32
	_ = windows.GetExitCodeProcess(pi.Process, &code)
	fmt.Printf("PROBE: child (normal-user token) exit code %d\n", code)
	return nil
}
