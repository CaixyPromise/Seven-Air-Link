// Package client is the shared API transport for CLI commands and diagnostics.
package client

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
	"runtime"
	"seven-air-link/internal/model"
	"seven-air-link/internal/secure"
	"strings"
	"time"
)

type Client struct {
	Config model.Config
	HTTP   *http.Client
}

func ValidateServer(s string) error {
	u, e := url.Parse(s)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("server must be an https origin without credentials, path, query or fragment")
	}
	return nil
}
func Load(path string) (model.Config, error) {
	var c model.Config
	info, e := os.Lstat(path)
	if e != nil {
		return c, e
	}
	if !info.Mode().IsRegular() {
		return c, errors.New("credential config must be a regular non-symlink file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return c, errors.New("credential file permissions are too broad; run chmod 600 on the config")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return c, e
	}
	if e = json.Unmarshal(b, &c); e != nil {
		return c, e
	}
	if c.Version != 1 || c.SubjectID == "" || (c.Role != "owner" && c.Role != "device" && c.Role != "member") || len(c.Token) < 32 {
		return c, errors.New("unsupported or incomplete credential config")
	}
	if e = ValidateServer(c.Server); e != nil {
		return c, e
	}
	if _, e = secure.NormalizePin(c.Fingerprint); e != nil {
		return c, e
	}
	return c, nil
}
func New(c model.Config) (*Client, error) {
	if e := ValidateServer(c.Server); e != nil {
		return nil, e
	}
	tc, e := secure.PinnedTLS(c.Fingerprint)
	if e != nil {
		return nil, e
	}
	transport := &http.Transport{TLSClientConfig: tc, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 8 * time.Second, IdleConnTimeout: 30 * time.Second, MaxIdleConns: 8, MaxIdleConnsPerHost: 4, ForceAttemptHTTP2: false}
	return &Client{Config: c, HTTP: &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Close() { c.HTTP.CloseIdleConnections() }
func (c *Client) Request(ctx context.Context, method, path string, v any) (*http.Response, error) {
	var body io.Reader
	if v != nil {
		b, e := json.Marshal(v)
		if e != nil {
			return nil, e
		}
		body = bytes.NewReader(b)
	}
	r, e := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(c.Config.Server, "/")+path, body)
	if e != nil {
		return nil, e
	}
	if v != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if c.Config.Token != "" {
		r.Header.Set("Authorization", "Bearer "+c.Config.Token)
	}
	return c.HTTP.Do(r)
}
func DecodeError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	var out struct {
		Error model.APIError `json:"error"`
	}
	if json.Unmarshal(b, &out) == nil && out.Error.Code != "" {
		return &out.Error
	}
	return &model.APIError{Code: "http_error", Message: fmt.Sprintf("server returned HTTP %d", resp.StatusCode), Retryable: resp.StatusCode >= 500}
}
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	resp, e := c.Request(ctx, method, path, in)
	if e != nil {
		return &model.APIError{Code: "connection_failed", Message: e.Error(), Retryable: !errors.Is(e, secure.ErrPin) && !errors.Is(e, secure.ErrCertificate)}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return DecodeError(resp)
	}
	if out == nil {
		_, e = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return e
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out)
}
