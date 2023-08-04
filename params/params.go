package params

type EventType string

const (
	EventTypeTunnelReady  EventType = "tunnel_ready"
	EventTypeTunnelClosed EventType = "tunnel_closed"
)

type TunnelEvent struct {
	EventType          EventType
	NotifyChan         chan string
	ErrorChan          chan error
	BindAddr           string
	RequestedSubdomain string
}
