// qp-installer: the double-click installer. On macOS it is the executable inside "Quietport Installer <code>.app";
// on Windows it is "Quietport-<code>.exe". It finds the invite code in its own file name (or asks for the link),
// fetches the client bundle and the personalised payload from the hub, and runs the same install as the shell path.
// No terminal, no administrator rights (C-2). Signed + notarized on macOS so Gatekeeper opens it without ceremony.
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"quietport.app/quietport/internal/agent"
)

var (
	Version     = "dev"
	DefaultHost = "quietport.app" // -ldflags -X main.DefaultHost=...
)

var codeRe = regexp.MustCompile(`[a-z2-7]{26}`)

var errNoBundled = errors.New("no bundled client")

func main() {
	agent.Version = Version
	host, code := findInvite()
	if len(os.Args) > 1 && os.Args[1] == "--print-invite" {
		fmt.Printf("host=%s code=%s\n", host, code)
		return
	}
	var startName, startFolder string
	if code == "" && agent.Installed() {
		switch askInstalled() {
		case "remove":
			keep := askKeepFolder()
			if err := agent.Uninstall(!keep); err != nil {
				fail("some files could not be removed.", "")
			}
			removed(keep)
			return
		case "cancel":
			os.Exit(0)
		}
		// "join": fall through and ask for a link
	}
	if code == "" {
		if askStartOrJoin() { // start a new folder
			startName = askText("What is your name?", "")
			if startName == "" {
				os.Exit(0)
			}
			startFolder = askText("Name your folder.", "Shared")
			if startFolder == "" {
				startFolder = "Shared"
			}
		} else {
			link := askLink()
			if link == "" {
				os.Exit(1)
			}
			host, code = parseLink(link)
			if code == "" {
				fail("that does not look like a Quietport invite link.", "")
			}
		}
	}
	if host == "" {
		host = DefaultHost
	}
	if !confirm("Quietport will set up your shared folder now. It takes about a minute and needs no password.") {
		os.Exit(0)
	}
	support := ""
	if startName != "" {
		if err := runStart(host, startName, startFolder, &support); err != nil {
			fail(err.Error(), support)
		}
	} else if err := run(host, code, &support); err != nil {
		fail(err.Error(), support)
	}
	done()
}

// runStart: open signup. Ask the hub for a fresh person + folder, then install exactly like an invite.
func runStart(host, name, folder string, support *string) error {
	app := agent.AppDir()
	if err := os.MkdirAll(app, 0o700); err != nil {
		return errors.New("the application folder could not be created.")
	}
	if err := installBundled(app); err != nil && err != errNoBundled {
		return errors.New("the bundled files could not be copied.")
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	body, _ := jsonMarshal(map[string]string{"name": name, "folder": folder})
	resp, err := client.Post(fmt.Sprintf("https://%s/j/new", host), "application/json", bytes.NewReader(body))
	if err != nil {
		return errors.New("the server could not be reached.")
	}
	defer resp.Body.Close()
	pl, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 201 {
		var e struct{ Error string }
		_ = json(pl, &e)
		if e.Error != "" {
			return errors.New(e.Error)
		}
		return errors.New("the server did not accept a new folder.")
	}
	var p struct {
		Code           string `json:"code"`
		SupportContact string `json:"support_contact"`
		OperatorName   string `json:"operator_name"`
	}
	if json(pl, &p) != nil || p.Code == "" {
		return errors.New("the server sent an unexpected reply.")
	}
	if p.SupportContact != "" {
		*support = p.OperatorName + " (" + p.SupportContact + ")"
	}
	plPath := filepath.Join(app, "payload.json")
	if err := os.WriteFile(plPath, pl, 0o600); err != nil {
		return errors.New("the invitation could not be saved.")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	return agent.Install(ctx, p.Code, plPath)
}

// findInvite reads the code from the bundle / exe name, and the host from the download-origin metadata when present.
func findInvite() (host, code string) {
	exe, err := os.Executable()
	if err != nil {
		return "", ""
	}
	candidates := []string{filepath.Base(exe)}
	if runtime.GOOS == "darwin" {
		// .../Quietport Installer <code>.app/Contents/MacOS/quietport-installer
		candidates = append(candidates, filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(exe)))))
	}
	for _, c := range candidates {
		if m := codeRe.FindString(strings.ToLower(c)); m != "" {
			code = m
			break
		}
	}
	if code == "" {
		code = mountedImageCode()
	}
	host = originHost(exe)
	return host, code
}

func parseLink(s string) (host, code string) {
	s = strings.TrimSpace(strings.ToLower(s))
	code = codeRe.FindString(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	if i := strings.Index(s, "/"); i > 0 {
		host = s[:i]
	}
	return host, code
}

type payload struct {
	SupportContact string `json:"support_contact"`
	OperatorName   string `json:"operator_name"`
}

func run(host, code string, support *string) error {
	app := agent.AppDir()
	if err := os.MkdirAll(app, 0o700); err != nil {
		return errors.New("the application folder could not be created.")
	}
	client := &http.Client{Timeout: 10 * time.Minute}
	// Everything the client needs ships inside the installer; the hub is only asked for the personal payload.
	if err := installBundled(app); err != nil {
		if err != errNoBundled {
			return errors.New("the bundled files could not be copied.")
		}
		arch := runtime.GOARCH
		var url string
		if runtime.GOOS == "windows" {
			url = fmt.Sprintf("https://%s/dl/quietport-windows-%s.zip", host, arch)
		} else {
			url = fmt.Sprintf("https://%s/dl/quietport-darwin-%s.tar.gz", host, arch)
		}
		b, err := get(client, url)
		if err != nil {
			return errors.New("the download did not complete.")
		}
		if err := extract(b, app); err != nil {
			return errors.New("the download was damaged.")
		}
	}
	pl, err := get(client, fmt.Sprintf("https://%s/j/%s/payload", host, code))
	if err != nil {
		return errors.New("this invitation link is no longer valid.")
	}
	var p payload
	if json(pl, &p) == nil && p.SupportContact != "" {
		*support = p.OperatorName + " (" + p.SupportContact + ")"
	}
	plPath := filepath.Join(app, "payload.json")
	if err := os.WriteFile(plPath, pl, 0o600); err != nil {
		return errors.New("the invitation could not be saved.")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	return agent.Install(ctx, code, plPath)
}

func get(c *http.Client, u string) ([]byte, error) {
	resp, err := c.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 400<<20))
}

func extract(b []byte, dir string) error {
	if runtime.GOOS == "windows" {
		zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			return err
		}
		for _, f := range zr.File {
			if f.FileInfo().IsDir() {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return err
			}
			out, err := os.OpenFile(filepath.Join(dir, filepath.Base(f.Name)), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
			if err != nil {
				rc.Close()
				return err
			}
			_, err = io.Copy(out, rc)
			out.Close()
			rc.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		out, err := os.OpenFile(filepath.Join(dir, filepath.Base(h.Name)), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, tr)
		out.Close()
		if err != nil {
			return err
		}
	}
}
