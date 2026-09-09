package main

import (
	"io"
	"os"
	"path/filepath"
)

// installBundled copies the client binaries that ship inside the app bundle (Contents/Resources/client, each one
// signed with the app) into the application folder. Returns errNoBundled if this build has none.
func installBundled(app string) error {
	exe, err := os.Executable()
	if err != nil {
		return errNoBundled
	}
	src := filepath.Join(filepath.Dir(filepath.Dir(exe)), "Resources", "client")
	entries, err := os.ReadDir(src)
	if err != nil || len(entries) == 0 {
		return errNoBundled
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		in, err := os.Open(filepath.Join(src, e.Name()))
		if err != nil {
			return err
		}
		out, err := os.OpenFile(filepath.Join(app, e.Name()), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			in.Close()
			return err
		}
		_, err = io.Copy(out, in)
		out.Close()
		in.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
