package httpapi

import (
	"context"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"seven-air-link/internal/model"
	"seven-air-link/internal/secure"
	"seven-air-link/internal/store"
	"strconv"
	"strings"
	"time"
)

type proxyGrant struct {
	Subject   store.Subject
	ServiceID string
	Revision  int64
	Expires   time.Time
}

// ConfigureProxy is called once, before either listener accepts requests.
func (s *Server) ConfigureProxy(controlPort, proxyPort int) {
	s.controlPort = controlPort
	s.proxyPort = proxyPort
}
func (s *Server) proxyTicket(w http.ResponseWriter, r *http.Request, sub store.Subject, v model.Service) {
	if s.proxyPort == 0 {
		s.deny(w, r, 503, "proxy_disabled", "start serve with --proxy-listen on a separate port")
		return
	}
	s.mu.Lock()
	svc, e := s.serviceForUseLocked(sub, v.ID)
	if e != nil {
		s.mu.Unlock()
		s.networkError(w, r, e)
		return
	}
	if svc.Protocol != "http" {
		s.mu.Unlock()
		s.deny(w, r, 400, "not_http_service", "use TCP local forwarding for this service")
		return
	}
	now := time.Now()
	for k, g := range s.proxyTickets {
		if now.After(g.Expires) {
			delete(s.proxyTickets, k)
		}
	}
	if len(s.proxyTickets) >= 128 {
		s.mu.Unlock()
		s.deny(w, r, 429, "ticket_quota", "too many proxy invitations")
		return
	}
	token := secure.Random("proxy_")
	expires := now.Add(2 * time.Minute)
	s.proxyTickets[secure.Hash(token)] = proxyGrant{Subject: sub, ServiceID: svc.ID, Revision: svc.Revision, Expires: expires}
	s.mu.Unlock()
	loginURL := "https://" + net.JoinHostPort(hostName(r.Host), strconv.Itoa(s.proxyPort)) + "/_bridge/login"
	s.audit(model.Event{ActorID: sub.ID, Action: "proxy.ticket_created", ResourceID: svc.ID, Result: "success"})
	jsonReply(w, 201, map[string]any{"ticket": token, "serviceId": svc.ID, "loginURL": loginURL, "expiresAt": expires, "note": "POST the ticket in a form, never place it in a URL. This grant can access only one service."})
}

var proxyLoginTemplate = template.Must(template.New("login").Parse(`<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Seven-Air-Link · 服务访问</title><style>body{font:16px system-ui;background:#f5f7fa;color:#16223b;max-width:600px;margin:10vh auto;padding:24px}article{background:white;padding:32px;border:1px solid #dde3ed;border-radius:16px}input,button{font:inherit;display:block;box-sizing:border-box;width:100%;margin:12px 0;padding:12px}code{display:block;overflow-wrap:anywhere}small{color:#596a80}</style><article><h1>服务访问入口</h1><p>这是独立的代理来源，不提供 Bridge 管理接口。</p><form method="post" action="/_bridge/login"><label>服务 ID<input name="serviceId" autocomplete="off" required></label><label>一次性服务凭证<input type="password" name="ticket" autocomplete="off" required></label><button>打开服务</button></form><small>在管理台“共享服务”中打开，或运行：</small><code>seven-air-link services open SERVICE --config CONFIG</code></article></html>`))

func (s *Server) ProxyHandler() http.Handler { return http.HandlerFunc(s.serveProxy) }
func (s *Server) serveProxy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Request-ID", secure.Random("req_")[:28])
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	h, p, e := net.SplitHostPort(r.Host)
	if e != nil || !s.hosts[strings.ToLower(h)] || p != strconv.Itoa(s.proxyPort) {
		s.deny(w, r, 421, "untrusted_host", "wrong proxy host or port")
		return
	}
	origin := r.Header.Get("Origin")
	proxyOrigin := "https://" + r.Host
	controlOrigin := "https://" + net.JoinHostPort(h, strconv.Itoa(s.controlPort))
	if origin != "" && origin != proxyOrigin && !(r.URL.Path == "/_bridge/login" && origin == controlOrigin) {
		s.deny(w, r, 403, "origin_denied", "cross-origin proxy operation denied")
		return
	}
	if (r.URL.Path == "/" || r.URL.Path == "/_bridge/login") && r.Method == "GET" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
		_ = proxyLoginTemplate.Execute(w, nil)
		return
	}
	if r.URL.Path == "/_bridge/login" && r.Method == "POST" {
		if !s.allowedRate("proxy-login:" + remoteIP(r)) {
			s.deny(w, r, 429, "rate_limited", "too many login attempts")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 8192)
		if e = r.ParseForm(); e != nil {
			s.deny(w, r, 400, "invalid_request", "invalid form")
			return
		}
		// Deliberately read POST body only; URL query never carries credentials.
		key := secure.Hash(r.PostForm.Get("ticket"))
		id := r.PostForm.Get("serviceId")
		s.mu.Lock()
		g, ok := s.proxyTickets[key]
		if ok {
			delete(s.proxyTickets, key)
		}
		now := time.Now()
		for k, old := range s.proxySessions {
			if now.After(old.Expires) {
				delete(s.proxySessions, k)
			}
		}
		if !ok || g.ServiceID != id || now.After(g.Expires) || len(s.proxySessions) >= 128 {
			s.mu.Unlock()
			s.deny(w, r, 403, "ticket_invalid_or_expired", "create a new service-specific ticket")
			return
		}
		v, err := s.serviceForUseLocked(g.Subject, id)
		if err != nil || v.Revision != g.Revision {
			s.mu.Unlock()
			s.deny(w, r, 403, "access_denied", "service permission changed")
			return
		}
		token := secure.Random("ps_")
		g.Expires = now.Add(30 * time.Minute)
		s.proxySessions[secure.Hash(token)] = g
		s.mu.Unlock()
		prefix := "/proxy/" + id + "/"
		http.SetCookie(w, &http.Cookie{Name: "slb_proxy_" + id, Value: token, Path: prefix, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 1800})
		http.Redirect(w, r, prefix, http.StatusSeeOther)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/proxy/") || r.Method == "CONNECT" {
		s.deny(w, r, 404, "not_found", "this listener only serves registered HTTP services")
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/proxy/"), "/", 2)
	if len(parts) != 2 || parts[0] == "" {
		s.deny(w, r, 404, "not_found", "expected /proxy/SERVICE_ID/path")
		return
	}
	id := parts[0]
	var sub store.Subject
	var expires time.Time
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		sub, e = s.st.Auth(strings.TrimPrefix(auth, "Bearer "))
	} else {
		e = store.ErrAuth
		if cookie, err := r.Cookie("slb_proxy_" + id); err == nil {
			s.mu.Lock()
			g, ok := s.proxySessions[secure.Hash(cookie.Value)]
			v, exists := s.st.Service(id)
			s.mu.Unlock()
			if ok && exists && g.ServiceID == id && time.Now().Before(g.Expires) && g.Revision == v.Revision {
				sub = g.Subject
				expires = g.Expires
				e = nil
			}
		}
	}
	if e != nil {
		s.deny(w, r, 401, "unauthenticated", "use a service-scoped browser ticket or an explicitly granted CLI identity")
		return
	}
	// Revoked devices may have old proxy cookies; cookies never bypass revocation.
	if sub.Role == "device" {
		d, ok := s.st.Device(sub.ID)
		if !ok || d.RevokedAt != nil {
			s.deny(w, r, 403, "device_revoked", "device authorization revoked")
			return
		}
	}
	s.mu.Lock()
	v, e := s.serviceForUseLocked(sub, id)
	s.mu.Unlock()
	if e != nil {
		s.networkError(w, r, e)
		return
	}
	if v.ID != id || v.Protocol != "http" {
		s.deny(w, r, 400, "invalid_service", "HTTP proxy requires a registered HTTP service ID")
		return
	}
	if !expires.IsZero() {
		ctx, cancel := context.WithDeadline(r.Context(), expires)
		defer cancel()
		r = r.WithContext(ctx)
	}
	prefix := "/proxy/" + id
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ForceAttemptHTTP2: false, ResponseHeaderTimeout: 15 * time.Second, MaxResponseHeaderBytes: 1 << 20,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return s.dialService(ctx, sub, id) }}
	defer transport.CloseIdleConnections()
	target := &url.URL{Scheme: "http", Host: v.Target}
	proxy := &httputil.ReverseProxy{Transport: transport, FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = "/" + parts[1]
			raw := strings.TrimPrefix(pr.In.URL.EscapedPath(), prefix)
			pr.Out.URL.RawPath = raw
			pr.Out.Host = v.Target
			pr.SetXForwarded()
			// This is an access proxy, not a credential forwarding proxy.
			// App cookies/authentication remain available via the TCP mapping instead.
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Del("Proxy-Authorization")
			for k := range pr.Out.Header {
				if strings.HasPrefix(strings.ToLower(k), "x-slb-") {
					pr.Out.Header.Del(k)
				}
			}
			pr.Out.Header.Del("Referer")
			if pr.Out.Header.Get("Origin") != "" {
				pr.Out.Header.Set("Origin", "http://"+v.Target)
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Del("Set-Cookie")
			resp.Header.Del("Service-Worker-Allowed")
			resp.Header.Add("Content-Security-Policy", "worker-src 'none'; frame-ancestors 'none'")
			if loc := resp.Header.Get("Location"); loc != "" {
				u, err := url.Parse(loc)
				if err == nil {
					if u.IsAbs() && u.Scheme == "http" && u.Host == v.Target {
						u.Scheme = ""
						u.Host = ""
						u.Path = prefix + u.Path
						u.RawPath = ""
						resp.Header.Set("Location", u.String())
					} else if u.Scheme == "" && u.Host == "" && strings.HasPrefix(u.Path, "/") {
						u.Path = prefix + u.Path
						u.RawPath = ""
						resp.Header.Set("Location", u.String())
					}
				}
			}
			return nil
		},
		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
			s.networkError(rw, req, networkErr(errorCode(err), "upstream unavailable; inspect device/service state"))
		},
	}
	proxy.ServeHTTP(w, r)
}
func (s *Server) proxyURLs(host string) map[string]string {
	if s.proxyPort == 0 {
		return nil
	}
	return map[string]string{"gateway": fmt.Sprintf("https://%s", net.JoinHostPort(host, strconv.Itoa(s.proxyPort)))}
}
