//go:build !darwin && !windows

package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

func confirm(msg string) bool { fmt.Println(msg); return true }
func askLink() string {
	fmt.Print("Paste your Quietport invite link: ")
	s, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(s)
}
func done()                    { fmt.Println("Quietport is connected.") }
func fail(msg, support string) { fmt.Println("Quietport could not be installed:", msg, "Please contact", support+"."); exit(1) }

func askStartOrJoin() bool { return false }
func askText(prompt, def string) string {
	fmt.Printf("%s [%s]: ", prompt, def)
	s, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	return s
}

func askInstalled() string {
	fmt.Print("Quietport is already set up here. [r]emove it, use a new [l]ink, or [c]ancel? ")
	s, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "r", "remove":
		return "remove"
	case "l", "link":
		return "join"
	}
	return "cancel"
}
func askKeepFolder() bool {
	fmt.Print("Keep your files in ~/QPSync? [Y/n] ")
	s, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return !strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "n")
}
func removed(keep bool) {
	if keep {
		fmt.Println("Quietport has been removed. Your files are still in ~/QPSync.")
	} else {
		fmt.Println("Quietport and ~/QPSync have been removed.")
	}
}
