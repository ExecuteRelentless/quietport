package main

import (
	_ "embed"
)

// bundle.tar.gz is placed here by scripts/build-installer.sh (the quietport-linux-<arch> client bundle).
//
//go:embed bundle.tar.gz
var bundleTar []byte

func installBundled(app string) error {
	if len(bundleTar) == 0 {
		return errNoBundled
	}
	return extract(bundleTar, app)
}
