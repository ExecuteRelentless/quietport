package main

import "testing"

// Two Quietport images mounted at once: the generic installer and a used invite's image left over from an earlier
// install. The installer takes a code only from the image its own executable runs from. On 2026-09-13 the generic
// installer took the used invite's code from the other image, skipped every question and failed at the payload.
const twoImages = "framework       : 683.160.3\n" +
	"driver          : 683.160.3\n" +
	"================================================\n" +
	"image-path      : /Users/pat/Downloads/Quietport-m3mbfu2q7l4kzx6yvw3ehp5tab.dmg\n" +
	"image-alias     : /Users/pat/Downloads/Quietport-m3mbfu2q7l4kzx6yvw3ehp5tab.dmg\n" +
	"shadow-path     : <none>\n" +
	"image-type      : read-only disk image\n" +
	"blockcount      : 700390\n" +
	"process ID      : 30650\n" +
	"/dev/disk4\tGUID_partition_scheme\t\n" +
	"/dev/disk4s1\t7C3457EF-0000-11AA-AA11-00306543ECAC\t\n" +
	"/dev/disk5\tEF57347C-0000-11AA-AA11-00306543ECAC\t\n" +
	"/dev/disk5s1\t41504653-0000-11AA-AA11-00306543ECAC\t/Volumes/Quietport Installer\n" +
	"================================================\n" +
	"image-path      : /Users/pat/Downloads/Quietport.dmg\n" +
	"image-alias     : /Users/pat/Downloads/Quietport.dmg\n" +
	"shadow-path     : <none>\n" +
	"image-type      : read-only disk image\n" +
	"blockcount      : 700390\n" +
	"process ID      : 30651\n" +
	"/dev/disk6\tGUID_partition_scheme\t\n" +
	"/dev/disk6s1\t7C3457EF-0000-11AA-AA11-00306543ECAC\t\n" +
	"/dev/disk7\tEF57347C-0000-11AA-AA11-00306543ECAC\t\n" +
	"/dev/disk7s1\t41504653-0000-11AA-AA11-00306543ECAC\t/Volumes/Quietport Installer 1\n"

func TestInstallerReadsTheCodeOnlyFromTheImageItRunsFrom(t *testing.T) {
	cases := []struct{ mount, want string }{
		{"/Volumes/Quietport Installer", "m3mbfu2q7l4kzx6yvw3ehp5tab"}, // the invite's image
		{"/Volumes/Quietport Installer 1", ""},                         // the generic image, beside the used one
		{"/", ""},                                                      // not on any image, whatever is mounted
		{"", ""},
	}
	for _, c := range cases {
		if got := imageCode(twoImages, c.mount); got != c.want {
			t.Errorf("exe on %q: code %q, want %q", c.mount, got, c.want)
		}
	}
	if got := imageCode("", "/Volumes/Quietport Installer"); got != "" {
		t.Errorf("no images mounted: code %q", got)
	}
}
