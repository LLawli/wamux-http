package waha

import (
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// DecodeRaw decodes an InboundMessage's raw_message: the serialized
// wa.Message the core relays. whatsapp-rust and whatsmeow share WhatsApp's
// own schema, so whatsmeow's generated types read it.
func DecodeRaw(raw []byte) (*waE2E.Message, error) {
	m := &waE2E.Message{}
	if len(raw) == 0 {
		return m, nil
	}
	return m, proto.Unmarshal(raw, m)
}
