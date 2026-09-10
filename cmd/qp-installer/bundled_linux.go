package main

import (
	"embed"
)

// bundle.tar.gz is placed here by scripts/build-installer.sh (the quietport-linux-<arch> client bundle).
//
// The bundle directory always exists (bundle/.keep) so the package compiles without a client build;
// scripts/build-installer.sh drops bundle.tar.gz into it for a release.
//
//go:embed all:bundle
var bundleFS embed.FS

var bundleTar, _ = bundleFS.ReadFile("bundle/bundle.tar.gz")

func installBundled(app string) error {
	if len(bundleTar) == 0 {
		return errNoBundled
	}
	return extract(bundleTar, app)
}
