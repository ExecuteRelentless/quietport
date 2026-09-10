package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/net/proxy"
)

// API is the operator HTTP client. It reaches the hub over the tailnet (through the local agent's SOCKS5 proxy)
// or over an ssh tunnel to 127.0.0.1:8443 during bootstrap.
type API struct {
	base, token, operator string
	http                  *http.Client
}

func NewAPI(base, token, socks, operator string) (*API, error) {
	tr := &http.Transport{IdleConnTimeout: 30 * time.Second}
	if socks != "" {
		u, err := url.Parse(socks)
		if err != nil {
			return nil, err
		}
		d, err := proxy.FromURL(u, proxy.Direct)
		if err != nil {
			return nil, err
		}
		tr.DialContext = d.(proxy.ContextDialer).DialContext
	}
	return &API{base: base, token: token, operator: operator, http: &http.Client{Transport: tr, Timeout: 120 * time.Second}}, nil
}

func (a *API) do(method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, a.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("X-Operator", a.operator)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		var e struct{ Error string }
		_ = json.Unmarshal(rb, &e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return errors.New(e.Error)
	}
	if out != nil && len(rb) > 0 {
		return json.Unmarshal(rb, out)
	}
	return nil
}

func (a *API) Get(p string, out any) error       { return a.do("GET", p, nil, out) }
func (a *API) Post(p string, in, out any) error  { return a.do("POST", p, in, out) }
func (a *API) Patch(p string, in, out any) error { return a.do("PATCH", p, in, out) }
func (a *API) Delete(p string) error             { return a.do("DELETE", p, nil, nil) }
func (a *API) DeleteOut(p string, out any) error { return a.do("DELETE", p, nil, out) }
func (a *API) Str(p string) (string, error) {
	var v any
	if err := a.Get(p, &v); err != nil {
		return "", err
	}
	return fmt.Sprint(v), nil
}
