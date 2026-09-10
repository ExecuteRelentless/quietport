package main

import (
	"embed"
)

// bundle.zip is placed here by scripts/build-installer.sh (the quietport-windows-amd64 client bundle).
//
// The bundle directory always exists (bundle/.keep) so the package compiles without a client build;
// scripts/build-installer.sh drops bundle.zip into it for a release.
//
//go:embed all:bundle
var bundleFS embed.FS

var bundleZip, _ = bundleFS.ReadFile("bundle/bundle.zip")

func installBundled(app string) error {
	if len(bundleZip) == 0 {
		return errNoBundled
	}
	return extract(bundleZip, app)
}
