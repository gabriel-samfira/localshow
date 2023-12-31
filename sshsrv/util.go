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

type consumer struct {
	wr             io.Writer
	loggingEnabled bool

	mux sync.Mutex
}

func (c *consumer) setLogging(enabled bool) {
	c.mux.Lock()
	defer c.mux.Unlock()

	c.loggingEnabled = enabled
}

func (c *consumer) Write(p []byte) (n int, err error) {
	if c.loggingEnabled {
		return c.wr.Write(p)
	}
	return len(p), nil
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

type messageHandler struct {
	msgChan   chan params.NotifyMessage
	errChan   chan error
	consumers map[string]*consumer
	urls      []byte

	format     messageFormat
	tlsEnabled bool

	ctx    context.Context
	mux    sync.Mutex
	quit   chan struct{}
	closed bool
	err    error
}

func (l *messageHandler) Register(wr io.Writer) string {
	l.mux.Lock()
	defer l.mux.Unlock()

	newUUID := uuid.New()
	l.consumers[newUUID.String()] = &consumer{wr: wr}
	return newUUID.String()
}

func (l *messageHandler) Urls(id string) {
	if id == "" {
		return
	}
	l.mux.Lock()
	defer l.mux.Unlock()

	wr, ok := l.consumers[id]
	if ok && len(l.urls) > 0 {
		wr.wr.Write(l.urls)
	}
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

	wr, ok := l.consumers[id]
	if ok {
		wr.setLogging(enabled)
	}
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

func (l *messageHandler) loop() {
	for {
		select {
		case <-l.ctx.Done():
			if !l.closed {
				l.closed = true
				close(l.quit)
			}
			return
		case <-l.quit:
			return
		case err := <-l.errChan:
			for _, consumer := range l.consumers {
				consumer.Write([]byte(color.Ize(color.Red, fmt.Sprintf("%s\n", err))))
			}
			l.err = err
			if !l.closed {
				l.closed = true
				close(l.quit)
			}
			return
		case msg, ok := <-l.msgChan:
			if !ok {
				return
			}
			var termMsg []byte
			var err error
			switch msg.MessageType {
			case params.NotifyMessageURL:
				termMsg, err = l.formatURLsMessage(msg.Payload, l.format)
				if err != nil {
					log.Printf("failed to format urls: %s", err)
					continue
				}
				l.urls = termMsg
			default:
				termMsg = msg.Payload
			}

			for _, consumer := range l.consumers {
				if len(termMsg) > 0 {
					consumer.Write([]byte(fmt.Sprintf("%s\n", termMsg)))
				}
			}
		}
	}
}

func (l *messageHandler) Close() {
	if !l.closed {
		l.closed = true
		close(l.quit)
	}
}

func (l *messageHandler) Wait() error {
	select {
	case <-l.quit:
	case <-l.ctx.Done():
	}
	return l.err
}
