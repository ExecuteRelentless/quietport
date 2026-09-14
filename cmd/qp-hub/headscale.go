package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Headscale is a thin wrapper over the headscale CLI (FR-80..84). The CLI talks to the daemon over its unix socket.
type Headscale struct{}

func (h Headscale) run(args ...string) ([]byte, error) {
	cmd := exec.Command("headscale", append(args, "-o", "json")...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("headscale %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

type hsUser struct {
	ID   json.Number `json:"id"`
	Name string      `json:"name"`
}

func (h Headscale) Users() ([]hsUser, error) {
	out, err := h.run("users", "list")
	if err != nil {
		return nil, err
	}
	var us []hsUser
	if err := json.Unmarshal(out, &us); err != nil {
		return nil, fmt.Errorf("headscale users list: %v: %s", err, out)
	}
	return us, nil
}

func (h Headscale) UserID(name string) (int64, error) {
	us, err := h.Users()
	if err != nil {
		return 0, err
	}
	for _, u := range us {
		if u.Name == name {
			id, _ := u.ID.Int64()
			return id, nil
		}
	}
	return 0, errors.New("headscale user not found: " + name)
}

func (h Headscale) UserCreate(name string) (int64, error) {
	if id, err := h.UserID(name); err == nil {
		return id, nil
	}
	if _, err := h.run("users", "create", name); err != nil {
		return 0, err
	}
	return h.UserID(name)
}

func (h Headscale) UserDestroy(id int64) error {
	_, err := h.run("users", "destroy", "--identifier", strconv.FormatInt(id, 10), "--force")
	return err
}

type hsPreAuthKey struct {
	ID  json.Number `json:"id"`
	Key string      `json:"key"`
}

// PreAuthKeyCreate: single-use, expires per ttl (FR-84).
func (h Headscale) PreAuthKeyCreate(userID int64, ttl time.Duration, tags []string) (hsPreAuthKey, error) {
	args := []string{"preauthkeys", "create", "--user", strconv.FormatInt(userID, 10), "--expiration", ttl.String()}
	if len(tags) > 0 {
		args = append(args, "--tags", strings.Join(tags, ","))
	}
	out, err := h.run(args...)
	if err != nil {
		return hsPreAuthKey{}, err
	}
	var k hsPreAuthKey
	if err := json.Unmarshal(out, &k); err != nil {
		return k, fmt.Errorf("preauthkey parse: %v: %s", err, out)
	}
	return k, nil
}

// PreAuthKeyExpire expires one pre-auth key. headscale 0.29 identifies the key by id alone; userID is kept in the
// signature for the callers' sake and is not sent (TestPreAuthKeyExpireUsesOnlyTheIDFlag).
func (h Headscale) PreAuthKeyExpire(userID, keyID int64) error {
	_ = userID
	_, err := h.run(preAuthKeyExpireArgs(keyID)...)
	return err
}

func preAuthKeyExpireArgs(keyID int64) []string {
	return []string{"preauthkeys", "expire", "--id", strconv.FormatInt(keyID, 10)}
}

type hsNode struct {
	ID          json.Number `json:"id"`
	Name        string      `json:"name"`
	GivenName   string      `json:"given_name"`
	IPAddresses []string    `json:"ip_addresses"`
	Tags        []string    `json:"tags"`
	User        hsUser      `json:"user"`
	Online      bool        `json:"online"`
}

func (h Headscale) Nodes(user string) ([]hsNode, error) {
	args := []string{"nodes", "list"}
	if user != "" {
		args = append(args, "--user", user)
	}
	out, err := h.run(args...)
	if err != nil {
		return nil, err
	}
	var ns []hsNode
	if err := json.Unmarshal(out, &ns); err != nil {
		return nil, fmt.Errorf("nodes parse: %v: %s", err, out)
	}
	return ns, nil
}

func (h Headscale) NodeByIP(ip string) (hsNode, bool) {
	ns, err := h.Nodes("")
	if err != nil {
		return hsNode{}, false
	}
	for _, n := range ns {
		for _, a := range n.IPAddresses {
			if a == ip {
				return n, true
			}
		}
	}
	return hsNode{}, false
}

func (h Headscale) NodeDelete(id int64) error {
	_, err := h.run("nodes", "delete", "--identifier", strconv.FormatInt(id, 10), "--force")
	return err
}

func (h Headscale) PolicySet(path string) error {
	_, err := exec.Command("headscale", "policy", "set", "-f", path).CombinedOutput()
	if err != nil {
		out, _ := exec.Command("headscale", "policy", "set", "-f", path).CombinedOutput()
		return fmt.Errorf("headscale policy set: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
