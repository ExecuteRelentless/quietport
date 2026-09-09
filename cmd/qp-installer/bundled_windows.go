package main

import (
	_ "embed"
)

// bundle.zip is placed here by scripts/build-installer.sh (the quietport-windows-amd64 client bundle).
//
//go:embed bundle.zip
var bundleZip []byte

func installBundled(app string) error {
	if len(bundleZip) == 0 {
		return errNoBundled
	}
	return extract(bundleZip, app)
}
