package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/net/proxy"

	"quietport.app/quietport/internal/model"
)

// HubClient talks to the hub's agent API through the userspace tailscaled SOCKS5 proxy.
type HubClient struct {
	base  string
	token string
	http  *http.Client
}

func NewHubClient(base, token, socksURL string) (*HubClient, error) {
	u, err := url.Parse(socksURL)
	if err != nil {
		return nil, err
	}
	d, err := proxy.FromURL(u, proxy.Direct)
	if err != nil {
		return nil, err
	}
	cd, ok := d.(proxy.ContextDialer)
	if !ok {
		return nil, errors.New("socks dialer has no context support")
	}
	tr := &http.Transport{DialContext: cd.DialContext, MaxIdleConns: 4, IdleConnTimeout: 60 * time.Second}
	return &HubClient{base: base, token: token, http: &http.Client{Transport: tr, Timeout: 90 * time.Second}}, nil
}

func (c *HubClient) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		var e struct{ Error string }
		_ = json.Unmarshal(rb, &e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return &HubError{Code: resp.StatusCode, Msg: e.Error}
	}
	if out != nil {
		return json.Unmarshal(rb, out)
	}
	return nil
}

type HubError struct {
	Code int
	Msg  string
}

func (e *HubError) Error() string { return fmt.Sprintf("hub %d: %s", e.Code, e.Msg) }

func (c *HubClient) Enrol(ctx context.Context, req model.EnrolRequest) (model.EnrolResponse, error) {
	var out model.EnrolResponse
	err := c.do(ctx, "POST", "/v1/enrol", req, &out)
	return out, err
}

func (c *HubClient) Heartbeat(ctx context.Context, hb model.Heartbeat) (model.ConfigBundle, error) {
	var out model.ConfigBundle
	err := c.do(ctx, "POST", "/v1/heartbeat", hb, &out)
	return out, err
}

func (c *HubClient) Ping(ctx context.Context) error { return c.do(ctx, "GET", "/v1/ping", nil, nil) }

// Download fetches a URL through the proxy (update payloads).
func (c *HubClient) Download(ctx context.Context, u string) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, nil, errors.New("download: " + resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 400<<20))
	return b, resp.Header, err
}
