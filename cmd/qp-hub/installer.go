package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

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
	sum, err := installerCRC(f, st)
	if err != nil {
		http.Error(w, "installer not readable", 500)
		return
	}
	fh := zip.FileHeader{Name: exeName, Method: zip.Store, CreatorVersion: 20, ReaderVersion: 20, CRC32: sum,
		CompressedSize64: uint64(st.Size()), UncompressedSize64: uint64(st.Size())}
	fh.SetModTime(st.ModTime()) // CreateRaw writes the MS-DOS date fields as given, and only SetModTime fills them
	head, tail, err := zipAround(fh, st.Size())
	if err != nil {
		http.Error(w, "installer not readable", 500)
		return
	}
	view := &zipView{head: head, body: f, size: st.Size(), tail: tail}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, zipName))
	// ServeContent answers Range and If-Range against the exe's time, so a broken download resumes (docs/adr/0026)
	http.ServeContent(w, r, "", st.ModTime(), io.NewSectionReader(view, 0, view.Len()))
}

// installerCRCs remembers the exe's CRC per file version, so a download costs one read of the exe, not two.
var installerCRCs sync.Map

func installerCRC(f *os.File, st os.FileInfo) (uint32, error) {
	key := fmt.Sprintf("%s %d %d", f.Name(), st.Size(), st.ModTime().UnixNano())
	if v, ok := installerCRCs.Load(key); ok {
		return v.(uint32), nil
	}
	crc := crc32.NewIEEE()
	if _, err := io.Copy(crc, io.NewSectionReader(f, 0, st.Size())); err != nil {
		return 0, err
	}
	installerCRCs.Store(key, crc.Sum32())
	return crc.Sum32(), nil
}

// zipView is a one-entry stored zip read in place: the headers before the entry and the central directory after it
// are in memory, the entry's bytes are read from the file.
type zipView struct {
	head []byte
	body io.ReaderAt
	size int64
	tail []byte
}

func (z *zipView) Len() int64 { return int64(len(z.head)) + z.size + int64(len(z.tail)) }

func (z *zipView) ReadAt(p []byte, off int64) (int, error) {
	n := 0
	for len(p) > 0 {
		hl, bl := int64(len(z.head)), z.size
		var src io.ReaderAt
		var base, end int64
		switch {
		case off < hl:
			src, base, end = bytes.NewReader(z.head), 0, hl
		case off < hl+bl:
			src, base, end = z.body, hl, hl+bl
		case off < z.Len():
			src, base, end = bytes.NewReader(z.tail), hl+bl, z.Len()
		default:
			return n, io.EOF
		}
		chunk := p[:min(int64(len(p)), end-off)]
		m, err := src.ReadAt(chunk, off-base)
		n, off, p = n+m, off+int64(m), p[m:]
		if m < len(chunk) {
			if err == nil || err == io.EOF {
				err = io.ErrUnexpectedEOF // the exe shrank under a running download
			}
			return n, err
		}
	}
	return n, nil
}

// zipAround returns the bytes a one-entry stored zip has before and after the entry's size bytes.
func zipAround(fh zip.FileHeader, size int64) (head, tail []byte, err error) {
	out := &skipWriter{}
	zw := zip.NewWriter(out)
	fw, err := zw.CreateRaw(&fh)
	if err != nil {
		return nil, nil, err
	}
	if err := zw.Flush(); err != nil { // the local header is out, so what is buffered so far is all of it
		return nil, nil, err
	}
	head = bytes.Clone(out.kept.Bytes())
	out.kept.Reset()
	out.skip = size
	if _, err := io.CopyN(fw, zeros{}, size); err != nil {
		return nil, nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, nil, err
	}
	return head, out.kept.Bytes(), nil
}

// skipWriter keeps what is written to it, except the next skip bytes.
type skipWriter struct {
	skip int64
	kept bytes.Buffer
}

func (w *skipWriter) Write(p []byte) (int, error) {
	k := min(int64(len(p)), w.skip)
	w.skip -= k
	w.kept.Write(p[k:])
	return len(p), nil
}

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
