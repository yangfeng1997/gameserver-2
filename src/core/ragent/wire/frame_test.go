package wire

import (
	"bytes"
	"testing"
)

func TestEncodeDecodeFrame(t *testing.T) {
	orig := Frame{
		Type:   FrameRpcRequest,
		Header: []byte("rpc-header-data"),
		Body:   []byte("rpc-body-data"),
	}
	data, err := EncodeFrame(orig)
	if err != nil {
		t.Fatalf("EncodeFrame: %v", err)
	}
	decoded, err := DecodeFrame(data)
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	if decoded.Type != orig.Type {
		t.Errorf("type=%d, want %d", decoded.Type, orig.Type)
	}
	if !bytes.Equal(decoded.Header, orig.Header) {
		t.Errorf("header=%q, want %q", decoded.Header, orig.Header)
	}
	if !bytes.Equal(decoded.Body, orig.Body) {
		t.Errorf("body=%q, want %q", decoded.Body, orig.Body)
	}
}

func TestEncodeDecodeFrameEmpty(t *testing.T) {
	orig := Frame{Type: FrameHeartbeat}
	data, err := EncodeFrame(orig)
	if err != nil {
		t.Fatalf("EncodeFrame: %v", err)
	}
	if len(data) != 4+3 {
		t.Errorf("empty heartbeat frame len=%d, want %d", len(data), 4+3)
	}
	decoded, err := DecodeFrame(data)
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	if decoded.Type != FrameHeartbeat {
		t.Errorf("type=%d, want FrameHeartbeat", decoded.Type)
	}
}

func TestDecodeFrameTooShort(t *testing.T) {
	_, err := DecodeFrame([]byte{0, 0, 0, 0, 0, 0})
	if err == nil {
		t.Error("expected error for too short frame")
	}
}

func TestEncodeFrameLargeHeader(t *testing.T) {
	bigHeader := make([]byte, 0x10000) // exceeds 16-bit
	_, err := EncodeFrame(Frame{Type: FrameRpcRequest, Header: bigHeader})
	if err == nil {
		t.Error("expected error for too large header")
	}
}

func TestEncodeDecodeRPCWireHeader(t *testing.T) {
	orig := RPCWireHeader{
		SeqID:       42,
		ServerType:  2,
		RoutingMode: uint8(RoutingModeHash),
		DeadlineMs:  3000,
		SrcNodeID:   100,
		DestNodeID:  200,
		ErrCode:     0,
		RoutingKey:  "player_1",
		Route:       "LobbyRemote/Test",
	}
	data := EncodeRPCWireHeader(orig)
	decoded, err := DecodeRPCWireHeader(data)
	if err != nil {
		t.Fatalf("DecodeRPCWireHeader: %v", err)
	}
	if decoded.SeqID != orig.SeqID {
		t.Errorf("SeqID=%d, want %d", decoded.SeqID, orig.SeqID)
	}
	if decoded.ServerType != orig.ServerType {
		t.Errorf("ServerType=%d, want %d", decoded.ServerType, orig.ServerType)
	}
	if decoded.RoutingMode != orig.RoutingMode {
		t.Errorf("RoutingMode=%d, want %d", decoded.RoutingMode, orig.RoutingMode)
	}
	if decoded.RoutingKey != orig.RoutingKey {
		t.Errorf("RoutingKey=%q, want %q", decoded.RoutingKey, orig.RoutingKey)
	}
	if decoded.Route != orig.Route {
		t.Errorf("Route=%q, want %q", decoded.Route, orig.Route)
	}
}

func TestAppendFrame(t *testing.T) {
	// Test that AppendFrame correctly appends to existing data
	dst := []byte("prefix")
	dst, err := AppendFrame(dst, Frame{Type: FrameRpcNotify, Body: []byte("payload")})
	if err != nil {
		t.Fatalf("AppendFrame: %v", err)
	}
	if !bytes.HasPrefix(dst, []byte("prefix")) {
		t.Errorf("dst does not preserve prefix: %q", dst)
	}
	// The frame should be at position 6 onwards
	frame, err := DecodeFrame(dst[6:])
	if err != nil {
		t.Fatalf("DecodeFrame of appended: %v", err)
	}
	if frame.Type != FrameRpcNotify {
		t.Errorf("type=%d, want FrameRpcNotify", frame.Type)
	}
	if !bytes.Equal(frame.Body, []byte("payload")) {
		t.Errorf("body=%q", frame.Body)
	}
}

func TestEncodeDecodeRouteBody(t *testing.T) {
	payload := []byte("hello")
	data := EncodeRouteBody(12345, payload)
	nodeID, body, err := DecodeRouteBody(data)
	if err != nil {
		t.Fatalf("DecodeRouteBody: %v", err)
	}
	if nodeID != 12345 {
		t.Errorf("nodeID=%d, want 12345", nodeID)
	}
	if !bytes.Equal(body, payload) {
		t.Errorf("body=%q, want %q", body, payload)
	}
}

func TestFrameTypes(t *testing.T) {
	types := map[FrameType]string{
		FrameHandshake:     "Handshake",
		FrameHandshakeAck:  "HandshakeAck",
		FrameRpcRequest:    "RpcRequest",
		FrameRpcResponse:   "RpcResponse",
		FrameRpcNotify:     "RpcNotify",
		FrameHeartbeat:     "Heartbeat",
		FrameBroadcastSent: "BroadcastSent",
	}
	for typ, name := range types {
		if int(typ) < 0x01 || int(typ) > 0x07 {
			t.Errorf("%s has invalid value %d", name, typ)
		}
	}
}
