// qpsync-agent: the Quietport client. Subcommands: run | install | join | resync | uninstall | status | version.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"quietport.app/quietport/internal/agent"
)

var Version = "dev"

func main() {
	agent.Version = Version
	if printsForCaller(runtime.GOOS, os.Args) {
		attachConsole()
	}
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "version":
		fmt.Println(Version)
	case "run":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := agent.Run(ctx); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "install":
		fs := flag.NewFlagSet("install", flag.ExitOnError)
		code := fs.String("code", "", "invite code")
		payload := fs.String("payload", "", "payload file")
		_ = fs.Parse(os.Args[2:])
		if *code == "" || *payload == "" {
			usage()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		if err := agent.Install(ctx, *code, *payload); err != nil {
			// FR-17/18: one plain sentence, a contact, never a stack trace
			contact := supportContact(*payload)
			fmt.Printf("Quietport could not be installed: %s Please contact %s.\n", err.Error(), contact)
			os.Exit(1)
		}
		fmt.Println("Quietport is connected.")
	case "join":
		// a computer that already has Quietport: hand the link to the running agent (docs/adr/0019)
		if len(os.Args) < 3 {
			usage()
		}
		names, err := agent.JoinInstalled(os.Args[2])
		if err != nil && len(names) == 0 {
			fmt.Printf("The folder could not be added: %s\n", sentence(err.Error()))
			os.Exit(1)
		}
		if err != nil {
			fmt.Println(sentence(err.Error()))
			return
		}
		fmt.Printf("You are in %s. It is in your QPSync folder.\n", strings.Join(names, ", "))
	case "resync":
		// one full sync for a folder that keeps being refused, keeping the copies the person chooses (docs/adr/0023)
		folder, keep := "", ""
		for i := 2; i < len(os.Args); i++ {
			switch a := os.Args[i]; {
			case a == "--keep" && i+1 < len(os.Args):
				keep = os.Args[i+1]
				i++
			case strings.HasPrefix(a, "--keep="):
				keep = strings.TrimPrefix(a, "--keep=")
			default:
				folder = a
			}
		}
		if folder == "" || keep == "" {
			fmt.Fprintln(os.Stderr, "usage: qpsync-agent resync <folder> --keep this|hub|newer\n  this   keep this computer's copies where they differ from the hub's\n  hub    keep the hub's copies\n  newer  keep whichever copy was changed last")
			os.Exit(2)
		}
		name, err := agent.AskForFullSync(folder, keep)
		if err != nil {
			fmt.Printf("No full sync was asked for: %s\n", sentence(err.Error()))
			os.Exit(1)
		}
		fmt.Printf("The next sync of %s will be a full sync. Where the two sides differ it keeps %s copies.\n", name, agent.KeepWords(keep))
	case "uninstall":
		fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
		rm := fs.Bool("remove-folder", false, "also delete ~/QPSync")
		_ = fs.Parse(os.Args[2:])
		if err := agent.Uninstall(*rm); err != nil {
			fmt.Println("Quietport could not be removed completely:", err)
			os.Exit(1)
		}
		fmt.Println("Quietport has been removed.")
	case "status":
		fmt.Print(agent.StatusText())
	default:
		usage()
	}
}

// sentence gives a message its full stop when it has none; hub errors carry one, page errors do not.
func sentence(s string) string {
	if strings.HasSuffix(s, ".") {
		return s
	}
	return s + "."
}

func supportContact(payload string) string {
	if b, err := os.ReadFile(payload); err == nil {
		var p struct {
			SupportContact string `json:"support_contact"`
			OperatorName   string `json:"operator_name"`
		}
		if json(b, &p) == nil && p.SupportContact != "" {
			return p.OperatorName + " (" + p.SupportContact + ")"
		}
	}
	return "the person who invited you"
}

func usage() {
	fmt.Fprintf(os.Stderr, "qpsync-agent %s (%s/%s)\nusage: qpsync-agent run | install --code C --payload F | join <link> | resync <folder> --keep this|hub|newer | uninstall [--remove-folder] | status | version\n", Version, runtime.GOOS, runtime.GOARCH)
	os.Exit(2)
}
