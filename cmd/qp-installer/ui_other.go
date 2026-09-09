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
