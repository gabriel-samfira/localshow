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

package params

import "encoding/json"

type EventType string
type NotifyMessageType string

const (
	EventTypeTunnelReady  EventType = "tunnel_ready"
	EventTypeTunnelClosed EventType = "tunnel_closed"
)

const (
	NotifyMessageLog NotifyMessageType = "log"
	NotifyMessageURL NotifyMessageType = "url"
	NofityMessageRaw NotifyMessageType = "raw"
)

type TunnelEvent struct {
	EventType          EventType
	NotifyChan         chan NotifyMessage
	ErrorChan          chan error
	BindAddr           string
	RequestedSubdomain string
}

type URLs struct {
	HTTP  string `json:"http"`
	HTTPS string `json:"https"`
}

type NotifyMessage struct {
	MessageType NotifyMessageType
	Payload     json.RawMessage
}
