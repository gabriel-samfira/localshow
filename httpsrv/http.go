// Copyright 2023 Gabriel Adrian Samfira
//
//    Licensed under the Apache License, Version 2.0 (the "License"); you may
//    not use this file except in compliance with the License. You may obtain
//    a copy of the License at
//
//         http://www.apache.org/licenses/LICENSE-2.0
//
//    Unless required by applicable law or agreed to in writing, software
//    distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
//    WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
//    License for the specific language governing permissions and limitations
//    under the License.

package httpsrv

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	_ "expvar"         // Register the expvar handlers
	_ "net/http/pprof" // Register the pprof handlers

	"github.com/gabriel-samfira/localshow/apiserver/controllers"
	"github.com/gabriel-samfira/localshow/apiserver/router"
	"github.com/gabriel-samfira/localshow/config"
	"github.com/gabriel-samfira/localshow/params"
)

// newProxyTransport returns an http.Transport with sensible defaults.
// When tlsSkipVerify is true, the transport accepts any backend certificate.
// This is safe because the backend connection goes over an SSH tunnel.
func newProxyTransport(tlsSkipVerify bool) *http.Transport {
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	if tlsSkipVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return transport
}

func NewHTTPServer(ctx context.Context, cfg *config.Config, tunnelEvents chan params.TunnelEvent, controller *controllers.APIController) (*HTTPServer, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	listener, err := net.Listen("tcp", cfg.HTTPServer.BindAddress())
	if err != nil {
		return nil, fmt.Errorf("failed to listen on %s: %w", cfg.HTTPServer.BindAddress(), err)
	}

	var tlsListener net.Listener
	if cfg.HTTPServer.UseTLS {
		tlsListener, err = net.Listen("tcp", cfg.HTTPServer.TLSBindAddress())
		if err != nil {
			return nil, fmt.Errorf("failed to listen on %s: %w", cfg.HTTPServer.TLSBindAddress(), err)
		}
	}

	var debugListener net.Listener
	if cfg.DebugServer.Enabled {
		debugListener, err = net.Listen("tcp", cfg.DebugServer.BindAddressString())
		if err != nil {
			return nil, fmt.Errorf("failed to listen on %s: %w", cfg.DebugServer.BindAddressString(), err)
		}
	}

	router := router.NewAPIRouter(controller)

	return &HTTPServer{
		listener:         listener,
		tlsListener:      tlsListener,
		debugListener:    debugListener,
		cfg:              cfg,
		tunEvents:        tunnelEvents,
		ctx:              ctx,
		rootServerRouter: router,
	}, nil
}

type proxyTarget struct {
	remote    *httputil.ReverseProxy
	subdomain string
	bindAddr  string
	bindPort  uint32
	msgChan   chan params.NotifyMessage
	errChan   chan error
}

func (p *proxyTarget) logRequest(r *http.Request) {
	if p.msgChan == nil {
		return
	}
	clientIP := r.RemoteAddr
	if ip, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		clientIP = ip
	}
	tm := time.Now().UTC()
	logMsg := fmt.Sprintf("%s - - %s \"%s %s %s\" %s %dus", clientIP,
		tm.Format("02/Jan/2006:15:04:05 -0700"),
		r.Method,
		r.URL.Path,
		r.Proto,
		r.UserAgent(),
		time.Since(tm))
	p.msgChan <- params.NotifyMessage{
		MessageType: params.NotifyMessageLog,
		Payload:     []byte(logMsg),
	}
}

type HTTPServer struct {
	listener         net.Listener
	tlsListener      net.Listener
	debugListener    net.Listener
	cfg              *config.Config
	tunEvents        chan params.TunnelEvent
	ctx              context.Context
	rootServerRouter http.Handler

	vhosts sync.Map // map[string]*proxyTarget

	srv      *http.Server
	debugSrv *http.Server
}

func (h *HTTPServer) tunnelSuccessURLs(subdomain string) ([]byte, error) {
	urls := params.URLs{}
	dom := fmt.Sprintf("%s.%s", subdomain, h.cfg.HTTPServer.DomainName)
	httpTunnel := fmt.Sprintf("http://%s", dom)
	if h.cfg.HTTPServer.BindPort != 80 {
		httpTunnel = fmt.Sprintf("%s:%d", httpTunnel, h.cfg.HTTPServer.BindPort)
	}

	urls.HTTP = httpTunnel

	if h.cfg.HTTPServer.UseTLS {
		httpsTunnel := fmt.Sprintf("https://%s", dom)
		if h.cfg.HTTPServer.TLSBindPort != 443 {
			httpsTunnel = fmt.Sprintf("%s:%d", httpsTunnel, h.cfg.HTTPServer.TLSBindPort)
		}
		urls.HTTPS = httpsTunnel
	}

	return json.Marshal(urls)
}

var portMap = map[uint32]string{
	80:  "http",
	443: "https",
}

func (h *HTTPServer) registerTunnel(event params.TunnelEvent) (err error) {
	defer func() {
		if err != nil {
			select {
			case event.ErrorChan <- err:
			case <-time.After(5 * time.Second):
			}
		}
	}()

	if strings.Contains(event.RequestedSubdomain, ".") {
		return fmt.Errorf("invalid subdomain %s", event.RequestedSubdomain)
	}

	dom := fmt.Sprintf("%s.%s", event.RequestedSubdomain, h.cfg.HTTPServer.DomainName)
	if _, loaded := h.vhosts.Load(dom); loaded {
		return fmt.Errorf("subdomain %s already registered", event.RequestedSubdomain)
	}

	remote, err := url.Parse(fmt.Sprintf("%s://%s", portMap[event.RequestedPort], event.BindAddr))
	if err != nil {
		return fmt.Errorf("failed to parse bind address %s: %w", event.BindAddr, err)
	}

	// Use Rewrite (not the legacy Director) so hop-by-hop headers such as
	// Connection: Upgrade are forwarded correctly, enabling WebSocket and
	// other HTTP upgrade protocols.
	reverseProxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(remote)
			// SetXForwarded appends to X-Forwarded-For (IP only),
			// and sets X-Forwarded-Host and X-Forwarded-Proto from
			// the inbound request.
			pr.SetXForwarded()

			clientIP, _, splitErr := net.SplitHostPort(pr.In.RemoteAddr)
			if splitErr == nil {
				pr.Out.Header.Set("X-Real-IP", clientIP)
			}

			if pr.In.TLS != nil {
				pr.Out.Header.Set("X-Forwarded-Port", fmt.Sprintf("%d", h.cfg.HTTPServer.TLSBindPort))
			} else {
				pr.Out.Header.Set("X-Forwarded-Port", fmt.Sprintf("%d", h.cfg.HTTPServer.BindPort))
			}

			// Rewrite Origin so CORS checks pass on the backend.
			origin := pr.In.Header.Get("Origin")
			if origin != "" {
				inHost := pr.In.Host
				if host, _, herr := net.SplitHostPort(inHost); herr == nil {
					inHost = host
				}
				origParsed, perr := url.Parse(origin)
				if perr == nil && origParsed.Hostname() == inHost {
					pr.Out.Header.Set("Origin", fmt.Sprintf("%s://%s", remote.Scheme, remote.Host))
				}
			}
		},
		// Flush immediately so Server-Sent Events and streamed
		// responses are not buffered.
		FlushInterval: -1,
		Transport:     newProxyTransport(event.RequestedPort == 443),
	}
	log.Printf("registering tunnel for %s", dom)

	urls, err := h.tunnelSuccessURLs(event.RequestedSubdomain)
	if err != nil {
		return fmt.Errorf("failed to get urls: %w", err)
	}
	event.NotifyChan <- params.NotifyMessage{
		MessageType: params.NotifyMessageURL,
		Payload:     urls,
	}
	// Register the vhost after the notify message is sent to the client. This ensures
	// that the first message that is sent through the channel is the URL message.
	h.vhosts.Store(dom, &proxyTarget{
		remote:    reverseProxy,
		subdomain: event.RequestedSubdomain,
		bindAddr:  event.BindAddr,
		bindPort:  event.RequestedPort,
		msgChan:   event.NotifyChan,
		errChan:   event.ErrorChan,
	})
	return nil
}

func (h *HTTPServer) unregisterTunnel(event params.TunnelEvent) error {
	dom := fmt.Sprintf("%s.%s", event.RequestedSubdomain, h.cfg.HTTPServer.DomainName)
	log.Printf("unregistering tunnel for %s", dom)
	if _, loaded := h.vhosts.LoadAndDelete(dom); !loaded {
		log.Printf("subdomain %s (%s) not registered", event.RequestedSubdomain, dom)
	}
	return nil
}

// extractHostname returns the hostname portion of a host or host:port string.
func extractHostname(hostport string) string {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		// No port present; hostport is already a bare hostname.
		return hostport
	}
	return host
}

func (h *HTTPServer) handlerFunc() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hostname := extractHostname(r.Host)
		if hostname == h.cfg.HTTPServer.DomainName {
			h.rootServerRouter.ServeHTTP(w, r)
			return
		}

		val, ok := h.vhosts.Load(hostname)
		if !ok {
			w.WriteHeader(http.StatusBadGateway)
			w.Write(badRequestHTML(hostname))
			return
		}
		p := val.(*proxyTarget)
		p.logRequest(r)
		// All header manipulation (X-Forwarded-*, X-Real-IP, Origin
		// rewriting) is handled inside the ReverseProxy Rewrite function.
		p.remote.ServeHTTP(w, r)
	}
}

func (h *HTTPServer) loop() {
	defer func() {
		if err := h.Stop(); err != nil {
			log.Printf("failed to stop http server: %s", err)
		}
		if h.listener != nil {
			h.listener.Close()
		}
		if h.tlsListener != nil {
			h.tlsListener.Close()
		}
		if h.debugListener != nil {
			h.debugListener.Close()
		}
	}()

	for {
		select {
		case <-h.ctx.Done():
			return
		case tunEvent, ok := <-h.tunEvents:
			if !ok {
				return
			}
			switch tunEvent.EventType {
			case params.EventTypeTunnelReady:
				if err := h.registerTunnel(tunEvent); err != nil {
					log.Printf("failed to register tunnel: %s", err)
				}
			case params.EventTypeTunnelClosed:
				if err := h.unregisterTunnel(tunEvent); err != nil {
					log.Printf("failed to unregister tunnel: %s", err)
				}
			default:
				log.Printf("unknown event type: %s", tunEvent.EventType)
			}
		}
	}
}

func (h *HTTPServer) startReverseProxy() error {
	srv := &http.Server{
		Handler:           h.handlerFunc(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	h.srv = srv

	go func() {
		if err := srv.Serve(h.listener); err != http.ErrServerClosed {
			log.Printf("failed to serve on http: %s", err)
		}
	}()

	go func() {
		if h.cfg.HTTPServer.UseTLS && h.tlsListener != nil {
			if err := srv.ServeTLS(h.tlsListener, h.cfg.HTTPServer.TLSConfig.CRT, h.cfg.HTTPServer.TLSConfig.Key); err != http.ErrServerClosed {
				log.Printf("failed to serve on HTTPS: %s", err)
			}
		}
	}()

	go h.loop()
	return nil
}

func (h *HTTPServer) startDebugServer() error {
	srv := &http.Server{
		Handler: http.DefaultServeMux,
	}
	h.debugSrv = srv

	go func() {
		if err := srv.Serve(h.debugListener); err != http.ErrServerClosed {
			log.Printf("failed to serve on http: %s", err)
		}
	}()
	return nil
}

func (h *HTTPServer) Start() error {
	if err := h.startReverseProxy(); err != nil {
		return fmt.Errorf("failed to start reverse proxy: %w", err)
	}

	if h.cfg.DebugServer.Enabled {
		if err := h.startDebugServer(); err != nil {
			return fmt.Errorf("failed to start debug server: %w", err)
		}
	}

	return nil
}

func (h *HTTPServer) Stop() error {
	if h.srv == nil {
		return nil
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer shutdownCancel()
	if err := h.srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("failed to shutdown http server: %w", err)
	}

	if h.debugSrv != nil {
		if err := h.debugSrv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("failed to shutdown debug server: %w", err)
		}
	}

	return nil
}
