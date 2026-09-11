package main

import (
	"archive/zip"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"quietport.app/quietport/internal/cryptobox"
)

// Double-click installers (the no-terminal path).
//   GET /j/<code>/QuietportInstaller.zip   -> the notarized "Quietport Installer.app" re-zipped under a folder name that
//                                            carries the code (the signature covers the contents, not the folder name)
//   GET /j/<code>/Quietport-<code>.exe      -> the Windows installer exe, served under a file name that carries the code
//   GET /j/<code>/Quietport-<code>.zip      -> the same exe inside a zip; the invite page links this one (docs/adr/0008)
// Both read the code from their own name at launch, then fetch /j/<code>/payload exactly like the shell installers.

func (h *Hub) macInstallerDir() string {
	return filepath.Join(h.cfg.ReleasesDir, "installer", "Quietport Installer.app")
}

func (h *Hub) installerAvailable(kind string) bool {
	if kind == "dmg" {
		_, err := os.Stat(filepath.Join(h.cfg.ReleasesDir, "quietport-installer-darwin.dmg"))
		return err == nil
	}
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

func (h *Hub) inviteInstallerWin(w http.ResponseWriter, r *http.Request, zipped bool) {
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
	w.Header().Set("Cache-Control", "no-store")
	if zipped {
		h.serveWinZip(w, r, "Quietport-"+code+".exe", "Quietport-"+code+".zip")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="Quietport-%s.exe"`, code))
	http.ServeFile(w, r, f)
}

// serveWinZip sends the Windows installer inside a zip, as exeName (docs/adr/0008). Browsers and Defender are harsher
// on a downloaded .exe than on an archive, and the exe inside keeps the name that carries the invite code. Stored, not
// deflated: the exe is mostly the already compressed client bundle, so deflate saved 7% for a second of CPU. The CRC
// and sizes sit in the local header (no data descriptor), which every unzipper reads, streaming ones included.
func (h *Hub) serveWinZip(w http.ResponseWriter, r *http.Request, exeName, zipName string) {
	f, err := os.Open(filepath.Join(h.cfg.ReleasesDir, "quietport-installer-windows-amd64.exe"))
	if err != nil {
		http.Error(w, "installer not published", 503)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.Error(w, "installer not readable", 500)
		return
	}
	fh := zip.FileHeader{Name: exeName, Method: zip.Store, CreatorVersion: 20, ReaderVersion: 20,
		CompressedSize64: uint64(st.Size()), UncompressedSize64: uint64(st.Size())}
	fh.SetModTime(st.ModTime()) // CreateRaw writes the MS-DOS date fields as given, and only SetModTime fills them
	if r.Method != http.MethodHead {
		crc := crc32.NewIEEE()
		if _, err := io.Copy(crc, f); err != nil {
			http.Error(w, "installer not readable", 500)
			return
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			http.Error(w, "installer not readable", 500)
			return
		}
		fh.CRC32 = crc.Sum32()
	}
	var size byteCounter
	_ = oneFileZip(&size, fh, zeros{}, st.Size()) // the headers are fixed-width, so the CRC value cannot change the length
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, zipName))
	w.Header().Set("Content-Length", strconv.FormatInt(int64(size), 10))
	if r.Method == http.MethodHead {
		return
	}
	_ = oneFileZip(w, fh, f, st.Size())
}

// oneFileZip writes a zip holding one entry; body supplies size bytes already in fh.Method's encoding.
func oneFileZip(w io.Writer, fh zip.FileHeader, body io.Reader, size int64) error {
	zw := zip.NewWriter(w)
	fw, err := zw.CreateRaw(&fh)
	if err != nil {
		return err
	}
	if _, err := io.CopyN(fw, body, size); err != nil {
		return err
	}
	return zw.Close()
}

type byteCounter int64

func (c *byteCounter) Write(p []byte) (int, error) { *c += byteCounter(len(p)); return len(p), nil }

type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

var _ = strings.ToLower

// inviteInstallerDMG: one file for the Mac. The notarized disk image is generic; the file name carries the code.
func (h *Hub) inviteInstallerDMG(w http.ResponseWriter, r *http.Request) {
	code, ok := h.inviteGate(w, r)
	if !ok {
		return
	}
	if _, live := h.db.InvitePeek(cryptobox.HashToken(code)); !live {
		h.invitePlain(w, "gone")
		return
	}
	f := filepath.Join(h.cfg.ReleasesDir, "quietport-installer-darwin.dmg")
	if !h.installerAvailable("dmg") {
		http.Error(w, "installer not published", 503)
		return
	}
	w.Header().Set("Content-Type", "application/x-apple-diskimage")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="Quietport-%s.dmg"`, code))
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, f)
}
