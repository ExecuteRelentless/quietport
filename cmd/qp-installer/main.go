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
	"quietport.app/quietport/internal/cryptobox"
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
	if agent.Installed() {
		// a computer that already has Quietport never installs again: a link adds a folder to what is here, and
		// everything else on the computer stays as it is (docs/adr/0019). Remove is the way to start over.
		if code == "" {
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
			link := askLink()
			if link == "" {
				os.Exit(1)
			}
			if _, code = parseLink(link); code == "" {
				joinFailed("that does not look like a Quietport invite link.")
			}
		}
		if !confirm("Quietport is already on this computer, so there is nothing to install. Add the folder from this link to it?") {
			os.Exit(0)
		}
		names, err := agent.JoinInstalled(code)
		if err != nil {
			if len(names) > 0 {
				joined(names, err.Error())
				return
			}
			joinFailed(err.Error())
		}
		joined(names, "")
		return
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
	msg := "Quietport will set up your shared folder now. It takes about a minute and needs no password."
	if runtime.GOOS == "windows" {
		// the Firewall asks about the daemon the first time it starts; Quietport works either way (docs/adr/0015)
		msg += " If Windows asks about network access, either answer is fine."
	}
	if !confirm(msg) {
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
	if err := installClient(app, host); err != nil {
		return err
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
		Code string `json:"code"`
	}
	if json(pl, &p) != nil || p.Code == "" {
		return errors.New("the server sent an unexpected reply.")
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

// sentence gives a message its full stop when it has none; hub errors carry one, page errors do not.
func sentence(s string) string {
	if strings.HasSuffix(s, ".") {
		return s
	}
	return s + "."
}

type payload struct {
	InviterName string `json:"inviter_name"`
}

func run(host, code string, support *string) error {
	app := agent.AppDir()
	if err := os.MkdirAll(app, 0o700); err != nil {
		return errors.New("the application folder could not be created.")
	}
	if !linkLive("https://"+host, code) {
		return errors.New("this invitation link is no longer valid.")
	}
	if err := installClient(app, host); err != nil {
		return err
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	pl, err := get(client, fmt.Sprintf("https://%s/j/%s/payload", host, code))
	if err != nil {
		return errors.New("this invitation link is no longer valid.")
	}
	var p payload
	if json(pl, &p) == nil && p.InviterName != "" {
		*support = p.InviterName + ", who sent you the link"
	}
	plPath := filepath.Join(app, "payload.json")
	if err := os.WriteFile(plPath, pl, 0o600); err != nil {
		return errors.New("the invitation could not be saved.")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	return agent.Install(ctx, code, plPath)
}

// clientSumArm64 and clientSumAmd64 pin the client a Mac installer installs: the sha256 of
// quietport-darwin-<arch>-<Version>.tar.gz, compiled into the notarized installer by scripts/build-installer.sh
// (docs/adr/0027). An installer built without them installs nothing it downloads.
var clientSumArm64, clientSumAmd64 string

// installClient puts the client programs into app: the ones this installer carries (Windows, Linux), or on a Mac the
// client bundle of this release for the chip it runs on.
func installClient(app, host string) error {
	switch err := installBundled(app); {
	case err == nil:
		return nil
	case err != errNoBundled:
		return errors.New("the bundled files could not be copied.")
	}
	return downloadClient(runtime.GOOS, runtime.GOARCH, "https://"+host, app)
}

// downloadClient fetches this installer's own client bundle from base and extracts it into app. A Mac bundle must
// match the sum built into the installer, or nothing is extracted. Windows release installers carry the client, and
// their download is the development fallback it has always been; a Linux installer without its client stops here.
func downloadClient(goos, arch, base, app string) error {
	var url, want string
	switch goos {
	case "darwin":
		url, want = fmt.Sprintf("%s/dl/quietport-darwin-%s-%s.tar.gz", base, arch, Version), clientSumAmd64
		if arch == "arm64" {
			want = clientSumArm64
		}
	case "windows":
		url = fmt.Sprintf("%s/dl/quietport-windows-%s.zip", base, arch)
	default:
		return errors.New("this installer carries no Quietport client.")
	}
	part := filepath.Join(app, "client-"+Version+".part")
	dl := agent.NewDownloader()
	var b []byte
	var err error
	for try := 1; try <= 5; try++ { // each try resumes where the last one stopped
		if b, _, err = dl.Download(context.Background(), url, part); err == nil || try == 5 {
			break
		}
		time.Sleep(time.Duration(try) * 2 * time.Second)
	}
	_ = os.Remove(part)
	if err != nil {
		return errors.New("the download did not complete.")
	}
	if goos == "darwin" && cryptobox.SHA256Hex(b) != want {
		return errors.New("the download is not the Quietport release this installer was made for.")
	}
	if err := extract(b, app); err != nil {
		return errors.New("the download was damaged.")
	}
	return nil
}

// linkLive asks the invite page whether a link still works, without using it up, so a dead link is known before the
// client is downloaded.
func linkLive(base, code string) bool {
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Get(fmt.Sprintf("%s/j/%s", base, code))
	if err != nil {
		return true // the payload request that follows reports an unreachable hub in its own words
	}
	resp.Body.Close()
	return resp.StatusCode != http.StatusNotFound
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
