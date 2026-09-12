// qpsync-agent: the Quietport client. Subcommands: run | install | uninstall | status | version.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"quietport.app/quietport/internal/agent"
)

var Version = "dev"

func main() {
	agent.Version = Version
	if attachesConsole(runtime.GOOS, os.Args) {
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
	fmt.Fprintf(os.Stderr, "qpsync-agent %s (%s/%s)\nusage: qpsync-agent run | install --code C --payload F | uninstall [--remove-folder] | status | version\n", Version, runtime.GOOS, runtime.GOARCH)
	os.Exit(2)
}
