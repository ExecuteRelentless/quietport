package main

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"net"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"quietport.app/quietport/internal/cryptobox"
	"quietport.app/quietport/internal/model"
)

var codeRe = regexp.MustCompile(`^[a-z2-7]{26}$`)

// simple per-IP rate limit for the invite paths (FR-106): 30 requests / 10 minutes
type limiter struct {
	mu sync.Mutex
	m  map[string][]time.Time
}

var inviteLimiter = &limiter{m: map[string][]time.Time{}}

func (l *limiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	keep := l.m[ip][:0]
	for _, t := range l.m[ip] {
		if now.Sub(t) < 10*time.Minute {
			keep = append(keep, t)
		}
	}
	if len(keep) >= 30 {
		l.m[ip] = keep
		return false
	}
	l.m[ip] = append(keep, now)
	return true
}

func clientIP(r *http.Request) string {
	if x := r.Header.Get("CF-Connecting-IP"); x != "" {
		return x
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	return ip
}

var tmpl = template.Must(template.New("").Funcs(template.FuncMap{"lower": strings.ToLower}).ParseFS(webFS, "web/invite/*.html"))

func (h *Hub) routesPublic(mux *http.ServeMux) {
	// The site claims only its own paths; everything else falls through to the Headscale proxy.
	// Go's "GET /" would swallow every path, so pages are registered one by one.
	_ = fs.WalkDir(h.site, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == "." {
			return nil
		}
		if d.IsDir() {
			mux.Handle("GET /"+p+"/", http.FileServerFS(h.site))
			return fs.SkipDir
		}
		switch {
		case p == "index.html":
			mux.HandleFunc("GET /{$}", h.sitePage(p))
		case strings.HasSuffix(p, ".html"):
			mux.HandleFunc("GET /"+strings.TrimSuffix(p, ".html"), h.sitePage(p))
		default:
			mux.Handle("GET /"+p, http.FileServerFS(h.site))
		}
		return nil
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /j/{code}", h.invitePage)
	mux.HandleFunc("GET /j/{code}/mac", h.inviteScript("mac"))
	mux.HandleFunc("GET /j/{code}/win", h.inviteScript("win"))
	mux.HandleFunc("GET /j/{code}/QuietportInstall.command", h.inviteScript("mac"))
	mux.HandleFunc("GET /j/{code}/QuietportInstall.cmd", h.inviteScript("wincmd"))
	mux.HandleFunc("GET /j/{code}/payload", h.invitePayload)
	mux.HandleFunc("GET /j/{code}/QuietportInstaller.zip", h.inviteInstallerMac)
	mux.HandleFunc("GET /j/{code}/{file}", func(w http.ResponseWriter, r *http.Request) {
		f := r.PathValue("file")
		switch {
		case strings.HasPrefix(f, "Quietport-") && strings.HasSuffix(f, ".exe"):
			h.inviteInstallerWin(w, r)
		case strings.HasPrefix(f, "Quietport-") && strings.HasSuffix(f, ".dmg"):
			h.inviteInstallerDMG(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("GET /dl/{file}", h.download)
}

func (h *Hub) inviteGate(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !inviteLimiter.allow(clientIP(r)) {
		http.Error(w, "Too many requests. Please try again in a few minutes.", 429)
		return "", false
	}
	code := strings.ToLower(r.PathValue("code"))
	if !codeRe.MatchString(code) {
		h.invitePlain(w, "gone")
		return "", false
	}
	return code, true
}

type invitePageData struct {
	OS        string // mac | win | other
	Host      string
	Code      string
	Operator  string
	Support   string
	Circles   []string
	Mac1      string
	Win1      string
	MacApp    bool // notarized installer published
	WinApp    bool
}

func detectOS(ua string) string {
	ua = strings.ToLower(ua)
	switch {
	case strings.Contains(ua, "windows"):
		return "win"
	case strings.Contains(ua, "mac os") || strings.Contains(ua, "macintosh"):
		return "mac"
	}
	return "other"
}

// invitePage: FR-102/103/105. Never reveals whether a code existed.
func (h *Hub) invitePage(w http.ResponseWriter, r *http.Request) {
	code, ok := h.inviteGate(w, r)
	if !ok {
		return
	}
	inv, live := h.db.InvitePeek(cryptobox.HashToken(code))
	if !live {
		h.invitePlain(w, "gone")
		return
	}
	var circles []string
	for _, cid := range inv.CircleIDs {
		if c, err := h.db.CircleByID(cid); err == nil {
			circles = append(circles, c.DisplayName)
		}
	}
	d := invitePageData{OS: detectOS(r.UserAgent()), Host: h.cfg.Host, Code: code, Operator: h.cfg.OperatorName, Support: h.cfg.SupportContact, Circles: circles}
	d.Mac1 = fmt.Sprintf(`curl -fsSL https://%s/j/%s/mac | sh`, h.cfg.Host, code)
	d.Win1 = fmt.Sprintf(`irm https://%s/j/%s/win | iex`, h.cfg.Host, code)
	d.MacApp, d.WinApp = h.installerAvailable("dmg"), h.installerAvailable("win")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "invite.html", d); err != nil {
		http.Error(w, "page error", 500)
		return
	}
	w.Write(buf.Bytes())
}

func (h *Hub) invitePlain(w http.ResponseWriter, kind string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(404)
	_ = tmpl.ExecuteTemplate(w, "gone.html", map[string]string{"Operator": h.cfg.OperatorName})
}

// inviteScript serves the personalised installer (FR-13). It does not consume the code; the payload fetch does.
func (h *Hub) inviteScript(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, ok := h.inviteGate(w, r)
		if !ok {
			return
		}
		if _, live := h.db.InvitePeek(cryptobox.HashToken(code)); !live {
			h.plainInstallerError(w, kind)
			return
		}
		name := map[string]string{"mac": "install-mac.sh", "win": "install-win.ps1", "wincmd": "install-win.cmd"}[kind]
		b, err := fs.ReadFile(webFS, "web/invite/"+name)
		if err != nil {
			http.Error(w, "missing installer template", 500)
			return
		}
		s := strings.NewReplacer("__HOST__", h.cfg.Host, "__CODE__", code, "__SUPPORT__", h.cfg.SupportContact, "__OPERATOR__", h.cfg.OperatorName).Replace(string(b))
		w.Header().Set("Cache-Control", "no-store")
		switch kind {
		case "mac":
			w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
		case "win":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		case "wincmd":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", `attachment; filename="QuietportInstall.cmd"`)
		}
		if strings.HasSuffix(r.URL.Path, ".command") {
			w.Header().Set("Content-Disposition", `attachment; filename="QuietportInstall.command"`)
		}
		w.Write([]byte(s))
	}
}

// plainInstallerError: a script that prints one sentence (FR-18/105) when the link is no longer valid.
func (h *Hub) plainInstallerError(w http.ResponseWriter, kind string) {
	msg := "This invitation link is no longer valid. Please ask " + h.cfg.OperatorName + " for a new one."
	w.Header().Set("Cache-Control", "no-store")
	if kind == "mac" {
		w.Header().Set("Content-Type", "text/x-shellscript")
		fmt.Fprintf(w, "#!/bin/sh\necho %q\nexit 1\n", msg)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintf(w, "Write-Host %q\nexit 1\n", msg)
}

// invitePayload consumes the code (FR-101/104) and returns the personalised material.
func (h *Hub) invitePayload(w http.ResponseWriter, r *http.Request) {
	code, ok := h.inviteGate(w, r)
	if !ok {
		return
	}
	inv, ok, err := h.db.InviteConsume(r.Context(), cryptobox.HashToken(code), clientIP(r))
	if err != nil || !ok {
		writeErr(w, 404, "This invitation link is no longer valid. Please ask "+h.cfg.OperatorName+" for a new one.")
		return
	}
	sealed := inv.SealedKeys
	var names []string
	for _, cid := range inv.CircleIDs {
		if c, err := h.db.CircleByID(cid); err == nil {
			names = append(names, c.DisplayName)
		}
	}
	p := model.InvitePayload{LoginServer: "https://" + h.cfg.Host, PreAuthKey: inv.PreAuthKey, HubAPI: "http://" + h.cfg.TailnetIP + ":" + h.cfg.AgentAPIPort,
		SupportContact: h.cfg.SupportContact, OperatorName: h.cfg.OperatorName, Circles: names, SealedKeys: sealed, AgentVersion: Version}
	h.db.Event("invite.retrieved", inv.PersonID, 0, fmt.Sprintf("%s from %s", inv.Prefix, clientIP(r)))
	h.db.Audit("system", "invite.retrieved", inv.PersonName, inv.Prefix)
	go h.notifyOperator(fmt.Sprintf("Quietport: %s opened their invitation", inv.PersonName), fmt.Sprintf("%s retrieved the installer for invite %s at %s.", inv.PersonName, inv.Prefix, time.Now().Format(time.RFC1123)))
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, p)
}

func (h *Hub) download(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(r.PathValue("file"))
	if !strings.HasPrefix(name, "quietport-") && name != "SHA256SUMS.signed" {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(h.cfg.ReleasesDir, name))
}

func (h *Hub) sitePage(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := fs.ReadFile(h.site, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Write(b)
	}
}
