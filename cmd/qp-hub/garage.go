package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"time"
)

// Garage admin API v2 client (FR-70/72/73). Runs against 127.0.0.1:3903 with the admin token from garage.toml.
type Garage struct {
	base  string
	token string
	http  *http.Client
}

func newGarage(configPath string) (*Garage, error) {
	b, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("garage config: %w", err)
	}
	tok := regexp.MustCompile(`(?m)^admin_token\s*=\s*"([^"]+)"`).FindSubmatch(b)
	addr := regexp.MustCompile(`(?m)^api_bind_addr\s*=\s*"(127\.0\.0\.1:\d+)"`).FindSubmatch(b)
	if tok == nil || addr == nil {
		return nil, errors.New("garage config: admin_token / admin api_bind_addr not found")
	}
	return &Garage{base: "http://" + string(addr[1]), token: string(tok[1]), http: &http.Client{Timeout: 30 * time.Second}}, nil
}

func (g *Garage) call(method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, g.base+path, body)
	req.Header.Set("Authorization", "Bearer "+g.token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("garage %s %s: %s: %s", method, path, resp.Status, bytes.TrimSpace(rb))
	}
	if out != nil && len(rb) > 0 {
		return json.Unmarshal(rb, out)
	}
	return nil
}

type garageBucket struct {
	ID            string   `json:"id"`
	GlobalAliases []string `json:"globalAliases"`
	Bytes         int64    `json:"bytes"`
	Objects       int64    `json:"objects"`
	Quotas        struct {
		MaxSize *int64 `json:"maxSize"`
	} `json:"quotas"`
	Keys []struct {
		AccessKeyID string `json:"accessKeyId"`
		Name        string `json:"name"`
	} `json:"keys"`
}

func (g *Garage) BucketInfo(alias string) (garageBucket, error) {
	var b garageBucket
	err := g.call("GET", "/v2/GetBucketInfo?globalAlias="+alias, nil, &b)
	return b, err
}

func (g *Garage) BucketCreate(alias string, maxSize int64) (garageBucket, error) {
	var b garageBucket
	if err := g.call("POST", "/v2/CreateBucket", map[string]any{"globalAlias": alias}, &b); err != nil {
		return b, err
	}
	if maxSize > 0 {
		if err := g.BucketSetQuota(b.ID, maxSize); err != nil {
			return b, err
		}
	}
	return b, nil
}

func (g *Garage) BucketSetQuota(id string, maxSize int64) error {
	var q any
	if maxSize > 0 {
		q = map[string]any{"maxSize": maxSize, "maxObjects": nil}
	} else {
		q = map[string]any{"maxSize": nil, "maxObjects": nil}
	}
	return g.call("POST", "/v2/UpdateBucket?id="+id, map[string]any{"quotas": q}, nil)
}

func (g *Garage) BucketDelete(id string) error {
	return g.call("POST", "/v2/DeleteBucket?id="+id, nil, nil)
}

type garageKey struct {
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
	Name            string `json:"name"`
}

// KeyCreate makes a key with the given name and grants it read+write (+owner if requested) on the bucket.
func (g *Garage) KeyCreate(name, bucketID string, owner bool) (garageKey, error) {
	var k garageKey
	if err := g.call("POST", "/v2/CreateKey", map[string]any{"name": name, "neverExpires": true}, &k); err != nil {
		return k, err
	}
	perm := map[string]any{"bucketId": bucketID, "accessKeyId": k.AccessKeyID, "permissions": map[string]bool{"read": true, "write": true, "owner": owner}}
	if err := g.call("POST", "/v2/AllowBucketKey", perm, nil); err != nil {
		return k, err
	}
	return k, nil
}

func (g *Garage) KeyDelete(accessKeyID string) error {
	if accessKeyID == "" {
		return nil
	}
	return g.call("POST", "/v2/DeleteKey?id="+accessKeyID, nil, nil)
}
