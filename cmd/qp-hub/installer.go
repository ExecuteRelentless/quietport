package main

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"quietport.app/quietport/internal/cryptobox"
)

// Double-click installers (the no-terminal path).
//   GET /j/<code>/QuietportInstaller.zip   -> the notarized "Quietport Installer.app" re-zipped under a folder name that
//                                            carries the code (the signature covers the contents, not the folder name)
//   GET /j/<code>/Quietport-<code>.exe      -> the Windows installer exe, served under a file name that carries the code
// Both read the code from their own name at launch, then fetch /j/<code>/payload exactly like the shell installers.

func (h *Hub) macInstallerDir() string {
	return filepath.Join(h.cfg.ReleasesDir, "installer", "Quietport Installer.app")
}

func (h *Hub) installerAvailable(kind string) bool {
	if kind == "mac" {
		_, err := os.Stat(filepath.Join(h.macInstallerDir(), "Contents", "MacOS"))
		return err == nil
	}
	_, err := os.Stat(filepath.Join(h.cfg.ReleasesDir, "quietport-installer-windows-amd64.exe"))
	return err == nil
}

func (h *Hub) inviteInstallerMac(w http.ResponseWriter, r *http.Request) {
	code, ok := h.inviteGate(w, r)
	if !ok {
		return
	}
	if _, live := h.db.InvitePeek(cryptobox.HashToken(code)); !live {
		h.invitePlain(w, "gone")
		return
	}
	root := h.macInstallerDir()
	if !h.installerAvailable("mac") {
		http.Error(w, "installer not published", 503)
		return
	}
	prefix := "Quietport Installer " + code + ".app"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="Quietport Installer.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	zw := zip.NewWriter(w)
	defer zw.Close()
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			return nil
		}
		name := prefix + "/" + filepath.ToSlash(rel)
		fh, err := zip.FileInfoHeader(info)
		if err != nil {
			return nil
		}
		fh.Name = name
		if info.IsDir() {
			fh.Name += "/"
			_, _ = zw.CreateHeader(fh)
			return nil
		}
		fh.Method = zip.Deflate
		wr, err := zw.CreateHeader(fh)
		if err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(wr, f)
		return err
	})
}

func (h *Hub) inviteInstallerWin(w http.ResponseWriter, r *http.Request) {
	code, ok := h.inviteGate(w, r)
	if !ok {
		return
	}
	if _, live := h.db.InvitePeek(cryptobox.HashToken(code)); !live {
		h.invitePlain(w, "gone")
		return
	}
	f := filepath.Join(h.cfg.ReleasesDir, "quietport-installer-windows-amd64.exe")
	if !h.installerAvailable("win") {
		http.Error(w, "installer not published", 503)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="Quietport-%s.exe"`, code))
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, f)
}

var _ = strings.ToLower
