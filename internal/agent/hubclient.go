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
	"os"
	"time"

	"golang.org/x/net/proxy"

	"quietport.app/quietport/internal/model"
)

// HubClient talks to the hub's agent API through the userspace tailscaled SOCKS5 proxy.
type HubClient struct {
	dl    *http.Client
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
	// dl has no overall deadline: an update is tens of MB and may crawl through a relay; Download watches for stalls instead
	return &HubClient{base: base, token: token, http: &http.Client{Transport: tr, Timeout: 90 * time.Second}, dl: &http.Client{Transport: tr}}, nil
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

// Download fetches a URL through the proxy into part (resumable with Range across attempts), and returns the whole
// file. It has no total deadline; it gives up only when no byte arrives for 3 minutes, so a slow relay link still
// finishes over a few heartbeats.
func (c *HubClient) Download(ctx context.Context, u, part string) ([]byte, http.Header, error) {
	var have int64
	if st, err := os.Stat(part); err == nil {
		have = st.Size()
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if have > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
	}
	cl := c.dl
	if cl == nil {
		cl = c.http
	}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	switch resp.StatusCode {
	case http.StatusPartialContent:
	case http.StatusOK:
		flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC // server ignored the range, start over
	case http.StatusRequestedRangeNotSatisfiable:
		_ = os.Remove(part)
		return nil, nil, errors.New("download: stale partial file removed, will retry")
	default:
		return nil, nil, errors.New("download: " + resp.Status)
	}
	f, err := os.OpenFile(part, flags, 0o600)
	if err != nil {
		return nil, nil, err
	}
	// stall watchdog: cancel the body read if nothing arrives for 3 minutes
	stall := time.AfterFunc(3*time.Minute, func() { resp.Body.Close() })
	_, err = io.Copy(f, io.LimitReader(&kick{r: resp.Body, t: stall, d: 3 * time.Minute}, 400<<20))
	stall.Stop()
	_ = f.Close()
	if err != nil {
		return nil, nil, fmt.Errorf("download (will resume): %w", err)
	}
	b, err := os.ReadFile(part)
	return b, resp.Header, err
}

// kick resets a timer on every successful read.
type kick struct {
	r io.Reader
	t *time.Timer
	d time.Duration
}

func (k *kick) Read(p []byte) (int, error) {
	n, err := k.r.Read(p)
	if n > 0 {
		k.t.Reset(k.d)
	}
	return n, err
}

func (c *HubClient) CreateInvite(ctx context.Context, req model.DeviceInviteRequest) (model.DeviceInviteResponse, error) {
	var out model.DeviceInviteResponse
	err := c.do(ctx, "POST", "/v1/invites", req, &out)
	return out, err
}
