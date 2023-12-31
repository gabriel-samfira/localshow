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
		vhosts:           map[string]*proxyTarget{},
		cfg:              cfg,
		tunEvents:        tunnelEvents,
		ctx:              ctx,
		mux:              &sync.Mutex{},
		rootServerRouter: router,
	}, nil
}

type proxyTarget struct {
	remote    *httputil.ReverseProxy
	remoteURI *url.URL
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
	tm := time.Now().UTC()
	logMsg := fmt.Sprintf("%s - - %s \"%s %s %s\" %s %dus", r.RemoteAddr,
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
	mux              *sync.Mutex
	rootServerRouter http.Handler

	vhosts map[string]*proxyTarget

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
	h.mux.Lock()
	defer h.mux.Unlock()
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
	if _, ok := h.vhosts[dom]; ok {
		return fmt.Errorf("subdomain %s already registered", event.RequestedSubdomain)
	}

	remote, err := url.Parse(fmt.Sprintf("%s://%s", portMap[event.RequestedPort], event.BindAddr))
	if err != nil {
		return fmt.Errorf("failed to parse bind address %s: %w", event.BindAddr, err)
	}

	reverseProxy := httputil.NewSingleHostReverseProxy(remote)
	if event.RequestedPort == 443 {
		// If TLS is enabled on the remote end, the certificate will most likely
		// not be valid for the SNI we're using to access it. Disable TLS verification.
		// Note: The connection between localshow and the remote end is over an encrypted
		// SSH channel. Using TLS does not really offer any additional security.
		reverseProxy.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
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
	h.vhosts[dom] = &proxyTarget{
		remote:    reverseProxy,
		remoteURI: remote,
		subdomain: event.RequestedSubdomain,
		bindAddr:  event.BindAddr,
		bindPort:  event.RequestedPort,
		msgChan:   event.NotifyChan,
		errChan:   event.ErrorChan,
	}
	return nil
}

func (h *HTTPServer) unregisterTunnel(event params.TunnelEvent) error {
	h.mux.Lock()
	defer h.mux.Unlock()

	dom := fmt.Sprintf("%s.%s", event.RequestedSubdomain, h.cfg.HTTPServer.DomainName)
	log.Printf("unregistering tunnel for %s", dom)
	if _, ok := h.vhosts[dom]; !ok {
		log.Printf("subdomain %s (%s) not registered", event.RequestedSubdomain, dom)
		return nil
	}

	delete(h.vhosts, dom)
	return nil
}

func (h *HTTPServer) handlerFunc() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parsed, err := url.Parse("http://" + r.Host)
		if err != nil {
			w.WriteHeader(404)
			return
		}
		if parsed.Hostname() == h.cfg.HTTPServer.DomainName {
			h.rootServerRouter.ServeHTTP(w, r)
			return
		}
		p, ok := h.vhosts[parsed.Hostname()]
		if !ok {
			w.WriteHeader(502)
			w.Write(badRequestHTML(parsed.Hostname()))
			return
		}
		r.Host = p.bindAddr
		origin := r.Header.Get("Origin")
		if origin != "" {
			origParsed, err := url.Parse(origin)
			if err == nil && origParsed.Hostname() == parsed.Hostname() {
				r.Header.Set("Origin", fmt.Sprintf("%s://%s", p.remoteURI.Scheme, p.remoteURI.Host))
			}
		}
		p.logRequest(r)
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
					tunEvent.ErrorChan <- err
				}
			case params.EventTypeTunnelClosed:
				if err := h.unregisterTunnel(tunEvent); err != nil {
					log.Printf("failed to unregister tunnel: %s", err)
					tunEvent.ErrorChan <- err
				}
			default:
				log.Printf("unknown event type: %s", tunEvent.EventType)
			}
		}
	}
}

func (h *HTTPServer) startReverseProxy() error {
	srv := &http.Server{
		Handler: h.handlerFunc(),
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
