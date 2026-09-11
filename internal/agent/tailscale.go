package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TS supervises one userspace tailscaled (FR-20/22/24) and wraps the tailscale CLI.
type TS struct {
	socksPort int
	mu        sync.Mutex
	cmd       *exec.Cmd
	stopping  bool
}

func NewTS(socksPort int) *TS { return &TS{socksPort: socksPort} }

func (t *TS) Args() []string {
	args := []string{"--tun=userspace-networking", "--socks5-server=127.0.0.1:" + strconv.Itoa(t.socksPort),
		"--state=" + TSDir() + string(os.PathSeparator) + "tailscaled.state", "--statedir=" + TSDir(), "--socket=" + TSSocket(),
		"--port=0", "--no-logs-no-support"}
	return args
}

// Run keeps tailscaled alive with exponential backoff capped at 5 minutes (FR-24).
func (t *TS) Run(ctx context.Context, logf func(string, ...any)) {
	_ = os.MkdirAll(TSDir(), 0o700)
	backoff := 2 * time.Second
	for ctx.Err() == nil {
		started := time.Now()
		cmd := exec.CommandContext(ctx, TailscaledBin(), t.Args()...)
		cmd.Stdout, cmd.Stderr = tsLogWriter{logf}, tsLogWriter{logf}
		hideWindow(cmd)
		t.mu.Lock()
		t.cmd = cmd
		t.mu.Unlock()
		err := cmd.Run()
		if ctx.Err() != nil {
			return
		}
		logf("tailscaled exited: %v", err)
		if time.Since(started) > 5*time.Minute {
			backoff = 2 * time.Second
		}
		time.Sleep(backoff)
		backoff *= 2
		if backoff > 5*time.Minute {
			backoff = 5 * time.Minute
		}
	}
}

type tsLogWriter struct{ logf func(string, ...any) }

func (w tsLogWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		// keep the log useful but small: drop the chatty lines
		if strings.Contains(line, "magicsock") || strings.Contains(line, "netcheck") || strings.Contains(line, "dns:") || strings.Contains(line, "Reconfig") {
			continue
		}
		w.logf("tailscaled: %s", line)
	}
	return len(p), nil
}

func (t *TS) cli(ctx context.Context, args ...string) ([]byte, error) {
	c, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, TailscaleBin(), append([]string{"--socket=" + TSSocket()}, args...)...)
	hideWindow(cmd)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err != nil {
		return out.Bytes(), fmt.Errorf("tailscale %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// Up logs in with a pre-auth key (install / re-provision). The key never appears in the returned error.
func (t *TS) Up(ctx context.Context, loginServer, authKey string) error {
	host, _ := os.Hostname()
	_, err := t.cli(ctx, upArgs(runtime.GOOS, loginServer, authKey, host)...)
	if err != nil && authKey != "" {
		return errors.New(strings.ReplaceAll(err.Error(), authKey, "[auth-key]"))
	}
	return err
}

// upArgs is the `tailscale up` command line for one platform.
func upArgs(goos, loginServer, authKey, host string) []string {
	args := []string{"up", "--reset", "--login-server=" + loginServer, "--accept-dns=false", "--accept-routes=false", "--hostname=" + tsHostname(host)}
	if authKey != "" {
		args = append(args, "--auth-key="+authKey)
	}
	if goos == "windows" {
		// keep the profile when no client is connected, and log back in after a restart (docs/adr/0011)
		args = append(args, "--unattended")
	}
	return args
}

// WaitReady waits for tailscaled to answer on its socket in any state, logged in or not (docs/adr/0010).
func (t *TS) WaitReady(ctx context.Context, max time.Duration) error {
	return waitForDaemon(ctx, func(ctx context.Context) error { _, err := t.Status(ctx, ""); return err }, max, 500*time.Millisecond)
}

// waitForDaemon polls probe until it succeeds, the deadline passes (the last probe error is returned) or ctx ends.
func waitForDaemon(ctx context.Context, probe func(context.Context) error, max, interval time.Duration) error {
	deadline := time.Now().Add(max)
	for {
		last := probe(ctx)
		if last == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("no answer within %s: %w", max, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

func tsHostname(h string) string {
	h = strings.ToLower(strings.TrimSuffix(h, ".local"))
	var b strings.Builder
	for _, r := range h {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		s = "device"
	}
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

type Status struct {
	BackendState string
	TailnetIP    string
	Online       bool
	HubRelayed   bool // FR-23
	HubSeen      bool
}

// Status reads `tailscale status --json`. hubIP identifies the peer whose path we report.
func (t *TS) Status(ctx context.Context, hubIP string) (Status, error) {
	out, err := t.cli(ctx, "status", "--json")
	if err != nil {
		return Status{}, err
	}
	var js struct {
		BackendState string `json:"BackendState"`
		Self         struct {
			TailscaleIPs []string `json:"TailscaleIPs"`
			Online       bool     `json:"Online"`
		} `json:"Self"`
		Peer map[string]struct {
			TailscaleIPs []string `json:"TailscaleIPs"`
			CurAddr      string   `json:"CurAddr"`
			Relay        string   `json:"Relay"`
			Online       bool     `json:"Online"`
			Active       bool     `json:"Active"`
		} `json:"Peer"`
	}
	if err := json.Unmarshal(out, &js); err != nil {
		return Status{}, err
	}
	st := Status{BackendState: js.BackendState, Online: js.Self.Online}
	for _, ip := range js.Self.TailscaleIPs {
		if net.ParseIP(ip) != nil && net.ParseIP(ip).To4() != nil {
			st.TailnetIP = ip
		}
	}
	for _, p := range js.Peer {
		for _, ip := range p.TailscaleIPs {
			if ip == hubIP {
				st.HubSeen = true
				st.HubRelayed = p.CurAddr == "" && p.Relay != ""
			}
		}
	}
	return st, nil
}

// WaitRunning waits for the backend to be Running with an IP.
func (t *TS) WaitRunning(ctx context.Context, max time.Duration) (Status, error) {
	deadline := time.Now().Add(max)
	var last Status
	for time.Now().Before(deadline) && ctx.Err() == nil {
		st, err := t.Status(ctx, "")
		if err == nil {
			last = st
			if st.BackendState == "Running" && st.TailnetIP != "" {
				return st, nil
			}
		}
		time.Sleep(1500 * time.Millisecond)
	}
	return last, errors.New("mesh did not come up (state " + last.BackendState + ")")
}

func (t *TS) Logout(ctx context.Context) { _, _ = t.cli(ctx, "logout") }

// ProxyURL is what rclone and the hub client use (FR-21).
func (t *TS) ProxyURL() string { return "socks5://127.0.0.1:" + strconv.Itoa(t.socksPort) }

func init() { log.SetFlags(0); _ = runtime.GOOS }

// FreePort picks an ephemeral loopback port at install (FR-22).
func FreePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 41055
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
