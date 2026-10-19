package waha

import (
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func TestDecodeRaw(t *testing.T) {
	raw, err := proto.Marshal(&waE2E.Message{LocationMessage: &waE2E.LocationMessage{DegreesLatitude: proto.Float64(-23.5), DegreesLongitude: proto.Float64(-46.6)}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := DecodeRaw(raw)
	if err != nil || m.GetLocationMessage().GetDegreesLatitude() != -23.5 {
		t.Fatalf("decode: %v %v", m, err)
	}
	if m, err := DecodeRaw(nil); err != nil || m == nil {
		t.Fatalf("empty raw: %v %v", m, err)
	}
}
