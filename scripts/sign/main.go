// sign: ed25519 helper for release signing. `go run ./scripts/sign -gen dir` makes a keypair; `-key file -msg text` signs.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"quietport.app/quietport/internal/cryptobox"
)

func main() {
	gen := flag.String("gen", "", "directory to write release.key + release.pub into")
	key := flag.String("key", "", "private key file")
	msg := flag.String("msg", "", "message to sign (sha256 hex)")
	flag.Parse()
	if *gen != "" {
		_ = os.MkdirAll(*gen, 0o700)
		pub, priv := cryptobox.NewSigningKey()
		if err := os.WriteFile(filepath.Join(*gen, "release.key"), []byte(priv+"\n"), 0o600); err != nil {
			panic(err)
		}
		if err := os.WriteFile(filepath.Join(*gen, "release.pub"), []byte(pub+"\n"), 0o644); err != nil {
			panic(err)
		}
		fmt.Println(pub)
		return
	}
	b, err := os.ReadFile(*key)
	if err != nil {
		panic(err)
	}
	sig, err := cryptobox.Sign(strings.TrimSpace(string(b)), []byte(*msg))
	if err != nil {
		panic(err)
	}
	fmt.Print(sig)
}
