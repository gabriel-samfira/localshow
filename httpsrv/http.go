package httpsrv

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/TwiN/go-color"
	"github.com/gabriel-samfira/localshow/config"
	"github.com/gabriel-samfira/localshow/params"
)

func NewHTTPServer(ctx context.Context, cfg *config.Config, tunnelEvents chan params.TunnelEvent) (*HTTPServer, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	listener, err := net.Listen("tcp", cfg.HTTPServer.BindAddress())
	if err != nil {
		return nil, fmt.Errorf("failed to listen on %s: %w", cfg.HTTPServer.BindAddress(), err)
	}

	return &HTTPServer{
		listener:  listener,
		vhosts:    map[string]*proxyTarget{},
		cfg:       cfg,
		tunEvents: tunnelEvents,
		ctx:       ctx,
		mux:       &sync.Mutex{},
	}, nil
}

type proxyTarget struct {
	remote    *httputil.ReverseProxy
	subdomain string
	bindAddr  string
	msgChan   chan string
	errChan   chan error
}

type HTTPServer struct {
	listener  net.Listener
	cfg       *config.Config
	tunEvents chan params.TunnelEvent
	ctx       context.Context
	mux       *sync.Mutex

	vhosts map[string]*proxyTarget

	srv *http.Server
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

	if _, ok := h.vhosts[event.RequestedSubdomain]; ok {
		return fmt.Errorf("subdomain %s already registered", event.RequestedSubdomain)
	}

	if strings.Contains(event.RequestedSubdomain, ".") {
		return fmt.Errorf("invalid subdomain %s", event.RequestedSubdomain)
	}

	dom := fmt.Sprintf("%s.%s", event.RequestedSubdomain, h.cfg.HTTPServer.DomainName)
	remote, err := url.Parse("http://" + event.BindAddr)
	if err != nil {
		return fmt.Errorf("failed to parse bind address %s: %w", event.BindAddr, err)
	}

	reverseProxy := httputil.NewSingleHostReverseProxy(remote)
	h.vhosts[dom] = &proxyTarget{
		remote:    reverseProxy,
		subdomain: event.RequestedSubdomain,
		bindAddr:  event.BindAddr,
		msgChan:   event.NotifyChan,
		errChan:   event.ErrorChan,
	}

	schema := "http"
	if h.cfg.HTTPServer.UseTLS {
		schema = "https"
	}
	event.NotifyChan <- fmt.Sprintf("Tunnel successfully created on %s", color.Ize(color.Green, fmt.Sprintf("%s://%s:%d", schema, dom, h.cfg.HTTPServer.BindPort)))
	return nil
}

func (h *HTTPServer) unregisterTunnel(event params.TunnelEvent) error {
	h.mux.Lock()
	defer h.mux.Unlock()

	if _, ok := h.vhosts[event.RequestedSubdomain]; !ok {
		return nil
	}

	delete(h.vhosts, event.RequestedSubdomain)
	return nil
}

func (h *HTTPServer) handlerFunc() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parsed, err := url.Parse("http://" + r.Host)
		if err != nil {
			w.WriteHeader(404)
			return
		}
		p, ok := h.vhosts[parsed.Hostname()]
		if !ok {
			w.WriteHeader(502)
			return
		}
		log.Println(r.URL)
		r.Host = p.bindAddr
		p.remote.ServeHTTP(w, r)
	}
}

func (h *HTTPServer) loop() {
	defer func() {
		if err := h.Stop(); err != nil {
			log.Printf("failed to stop http server: %s", err)
		}
		h.listener.Close()
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

func (h *HTTPServer) Start() error {
	srv := &http.Server{
		Handler: h.handlerFunc(),
	}
	h.srv = srv

	go func() {
		if h.cfg.HTTPServer.UseTLS {
			if err := srv.ServeTLS(h.listener, h.cfg.HTTPServer.TLSConfig.CRT, h.cfg.HTTPServer.TLSConfig.Key); err != http.ErrServerClosed {
				log.Printf("failed to serve: %s", err)
			}
		} else {
			if err := srv.Serve(h.listener); err != http.ErrServerClosed {
				log.Printf("failed to serve: %s", err)
			}
		}
	}()

	go h.loop()

	return nil
}

func (h *HTTPServer) Stop() error {
	if h.srv == nil {
		return nil
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer shutdownCancel()
	return h.srv.Shutdown(shutdownCtx)
}

// import (
// 	"log"
// 	"net/http"
// 	"net/http/httputil"
// 	"net/url"
// )

// func main() {
// 	var vhosts = map[string]*httputil.ReverseProxy{}
// 	remote, err := url.Parse("http://127.0.0.1:9897")
// 	if err != nil {
// 		panic(err)
// 	}
// 	proxy := httputil.NewSingleHostReverseProxy(remote)

// 	vhosts["analytics.samfira.com"] = proxy

// 	handler := func(w http.ResponseWriter, r *http.Request) {
// 		parsed, err := url.Parse("http://" + r.Host)
// 		if err != nil {
// 			w.WriteHeader(404)
// 			return
// 		}
// 		p, ok := vhosts[parsed.Hostname()]
// 		if !ok {
// 			w.WriteHeader(404)
// 			return
// 		}
// 		log.Println(r.URL)
// 		r.Host = remote.Host
// 		p.ServeHTTP(w, r)
// 	}

// 	// ssl_certificate /etc/letsencrypt/live/analytics.samfira.com/fullchain.pem; # managed by Certbot
// 	// ssl_certificate_key /etc/letsencrypt/live/analytics.samfira.com/privkey.pem; # managed by Certbot

// 	http.HandleFunc("/", handler)
// 	err = http.ListenAndServeTLS(":9898", "/etc/letsencrypt/live/analytics.samfira.com/fullchain.pem", "/etc/letsencrypt/live/analytics.samfira.com/privkey.pem", nil)
// 	if err != nil {
// 		panic(err)
// 	}
// }