package agent

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"quietport.app/quietport/internal/model"
)

// StatusText: FR-58, human-readable for support calls. Deliberately names no vendor (NFR-32).
func StatusText() string {
	var b strings.Builder
	store, err := OpenStore()
	if err != nil {
		return "Quietport is not installed on this account.\n"
	}
	cfg := store.Config()
	st := LoadState()
	fmt.Fprintf(&b, "Quietport %s\n", Version)
	if cfg.DeviceID == 0 {
		b.WriteString("Not connected: installation did not finish.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "Device #%d, folder %s\n", cfg.DeviceID, SyncRoot())
	if st.LastHeartbeat.IsZero() {
		b.WriteString("Hub: never reached\n")
	} else {
		ok := "ok"
		if !st.LastHeartbeatOK {
			ok = "FAILED"
		}
		fmt.Fprintf(&b, "Hub: last contact %s ago (%s), connection %s\n", ago(st.LastHeartbeat), ok, st.ConnectionType)
	}
	if st.Paused != "" {
		fmt.Fprintf(&b, "Paused: %s\n", st.Paused)
	}
	if len(st.Conditions) > 0 {
		fmt.Fprintf(&b, "Conditions: %s\n", strings.Join(st.Conditions, ", "))
	}
	fmt.Fprintf(&b, "Errors since start: %d (started %s ago)\n", st.ErrorCount, ago(st.StartedAt))
	for _, c := range cfg.Circles {
		h := st.Circles[c.Slug]
		last := "never"
		if !h.LastSync.IsZero() {
			last = ago(h.LastSync) + " ago"
		}
		fmt.Fprintf(&b, "  %-24s last sync %-12s %s\n", c.folder()+"/", last, folderState(c, h))
	}
	return b.String()
}

// folderState: one folder's state as a person reads it on a support call. A refused folder gets the command that
// settles it, with the helper's full path, because the helper is not on the PATH (docs/adr/0023).
func folderState(c CircleState, h model.CircleHealth) string {
	switch {
	case c.Removed:
		return "no longer shared with this device"
	case c.NeedsKey:
		return "needs a new invitation"
	case c.Refused:
		helper := filepath.Join(AppDir(), "qp")
		if runtime.GOOS == "windows" {
			helper += ".cmd"
		}
		return fmt.Sprintf("refused %d times in a row: %s. It waits for a person: %q resync %q --keep this|hub|newer", h.Failures, h.LastError, helper, c.folder())
	case h.LastError != "":
		return "last attempt failed: " + h.LastError
	case h.LastSync.IsZero():
		return "not synced yet"
	}
	return "ok"
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
