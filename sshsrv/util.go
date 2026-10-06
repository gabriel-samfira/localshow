package sshsrv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sync"
	"text/template"

	"github.com/TwiN/go-color"
	"github.com/google/uuid"

	"github.com/gabriel-samfira/localshow/params"
)

type messageFormat string

const (
	stringFormat messageFormat = "string"
	jsonFormat   messageFormat = "json"
)

// consumer is a terminal session attached to the message stream of one
// SSH connection. Access is guarded by messageHandler.mux.
type consumer struct {
	wr             io.Writer
	loggingEnabled bool
}

func newMessageHandler(ctx context.Context, msgs chan params.NotifyMessage, errs chan error, format messageFormat, tlsEnabled bool) *messageHandler {
	han := &messageHandler{
		msgChan:    msgs,
		errChan:    errs,
		format:     format,
		tlsEnabled: tlsEnabled,
		quit:       make(chan struct{}),
		consumers:  map[string]*consumer{},
		ctx:        ctx,
	}

	go han.loop()
	return han
}

// messageHandler fans tunnel notifications out to the terminal sessions
// of one SSH connection.
//
// Tunnel banners and errors are always delivered. The SSH client sends the
// port forwarding request and the session open back to back, so a session
// may attach either before or after its tunnel is registered. Both orders
// are handled under a single lock: a session that is already attached
// receives banners as they arrive, and a session attaching later gets a
// replay of everything it missed. Every session therefore sees each banner
// exactly once.
//
// Request log lines are opt-in per session (see SetLogging) and are never
// replayed.
type messageHandler struct {
	msgChan   chan params.NotifyMessage
	errChan   chan error
	consumers map[string]*consumer
	// banners holds every tunnel banner delivered so far, in order, for
	// replay to sessions that attach later.
	banners []byte
	// err is the tunnel error that terminated this handler, if any.
	err error

	format     messageFormat
	tlsEnabled bool

	ctx  context.Context
	mux  sync.Mutex
	quit chan struct{}
	once sync.Once
}

// Register attaches a terminal session and immediately replays the banners
// and any error it would have seen had it been attached from the start. It
// returns the session ID used by SetLogging and Unregister.
func (l *messageHandler) Register(wr io.Writer) string {
	l.mux.Lock()
	defer l.mux.Unlock()

	id := uuid.New().String()
	l.consumers[id] = &consumer{wr: wr}
	if len(l.banners) > 0 {
		wr.Write(l.banners)
	}
	if l.err != nil {
		wr.Write(formatError(l.err))
	}
	return id
}

func (l *messageHandler) Unregister(id string) {
	l.mux.Lock()
	defer l.mux.Unlock()

	delete(l.consumers, id)
}

func (l *messageHandler) SetLogging(id string, enabled bool) {
	if id == "" {
		return
	}
	l.mux.Lock()
	defer l.mux.Unlock()

	if c, ok := l.consumers[id]; ok {
		c.loggingEnabled = enabled
	}
}

func formatError(err error) []byte {
	return []byte(color.Ize(color.Red, fmt.Sprintf("%s\n", err)))
}

func (l *messageHandler) formatURLsMessage(urls json.RawMessage, format messageFormat) ([]byte, error) {
	if format == jsonFormat {
		return urls, nil
	}

	urlsObj := params.URLs{}
	if err := json.Unmarshal(urls, &urlsObj); err != nil {
		return nil, fmt.Errorf("failed to unmarshal urls: %w", err)
	}

	urlsObj.HTTP = color.Ize(color.Green, urlsObj.HTTP)
	if l.tlsEnabled {
		urlsObj.HTTPS = color.Ize(color.Green, urlsObj.HTTPS)
	}

	tpl, err := template.New("").Parse(tunnelSuccessfulBannerTemplate)
	if err != nil {
		return nil, fmt.Errorf("failed to parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, urlsObj); err != nil {
		return nil, fmt.Errorf("failed to execute template: %w", err)
	}

	return buf.Bytes(), nil
}

// line returns p as a line of terminal output, as a fresh slice so the
// caller's buffer is never aliased.
func line(p []byte) []byte {
	return []byte(fmt.Sprintf("%s\n", p))
}

// writeLocked writes p to the attached sessions. Lines flagged as logs
// only reach sessions that opted in. The caller must hold l.mux.
func (l *messageHandler) writeLocked(p []byte, isLog bool) {
	for _, c := range l.consumers {
		if isLog && !c.loggingEnabled {
			continue
		}
		c.wr.Write(p)
	}
}

// deliverBanner records a tunnel banner and writes it to every attached
// session. Recording and writing happen under the same lock as Register,
// so a session attaching concurrently gets the banner exactly once.
func (l *messageHandler) deliverBanner(banner []byte) {
	l.mux.Lock()
	defer l.mux.Unlock()

	l.banners = append(l.banners, banner...)
	l.writeLocked(banner, false)
}

// deliverError records the tunnel error and writes it to every attached
// session.
func (l *messageHandler) deliverError(err error) {
	l.mux.Lock()
	defer l.mux.Unlock()

	l.err = err
	l.writeLocked(formatError(err), false)
}

func (l *messageHandler) loop() {
	for {
		select {
		case <-l.ctx.Done():
			l.Close()
			return
		case <-l.quit:
			return
		case err := <-l.errChan:
			l.deliverError(err)
			l.Close()
			return
		case msg, ok := <-l.msgChan:
			if !ok {
				return
			}
			switch msg.MessageType {
			case params.NotifyMessageURL:
				banner, err := l.formatURLsMessage(msg.Payload, l.format)
				if err != nil {
					log.Printf("failed to format urls: %s", err)
					continue
				}
				l.deliverBanner(line(banner))
			case params.NotifyMessageLog:
				l.mux.Lock()
				l.writeLocked(line(msg.Payload), true)
				l.mux.Unlock()
			default:
				l.mux.Lock()
				l.writeLocked(line(msg.Payload), false)
				l.mux.Unlock()
			}
		}
	}
}

func (l *messageHandler) Close() {
	l.once.Do(func() {
		close(l.quit)
	})
}

func (l *messageHandler) Wait() error {
	select {
	case <-l.quit:
	case <-l.ctx.Done():
	}
	l.mux.Lock()
	defer l.mux.Unlock()
	return l.err
}
