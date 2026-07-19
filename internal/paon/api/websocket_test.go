package api

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"net"
	"strings"
	"testing"
)

func TestWebSocketAcceptKeyMatchesRFCExample(t *testing.T) {
	got := websocketAcceptKey("dGhlIHNhbXBsZSBub25jZQ==")
	if got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("accept key = %q", got)
	}
}

func TestWebSocketHandshakeEchoesAccessTokenSubprotocol(t *testing.T) {
	response := websocketHandshakeResponse("dGhlIHNhbXBsZSBub25jZQ==", " websocket-token, ignored ")
	for _, want := range []string{
		"HTTP/1.1 101 Switching Protocols\r\n",
		"Sec-WebSocket-Accept: s3pPLMBiTxaQ9kYGzzhZRbK+xOo=\r\n",
		"Sec-WebSocket-Protocol: websocket-token\r\n",
		"\r\n\r\n",
	} {
		if !strings.Contains(response, want) {
			t.Fatalf("handshake response missing %q:\n%s", want, response)
		}
	}

	withoutProtocol := websocketHandshakeResponse("dGhlIHNhbXBsZSBub25jZQ==", "")
	if strings.Contains(withoutProtocol, "Sec-WebSocket-Protocol:") {
		t.Fatalf("handshake without requested protocol should not select one:\n%s", withoutProtocol)
	}
}

func TestReadWebSocketFrameUnmasksClientText(t *testing.T) {
	frame := maskedClientFrame(wsOpcodeText, []byte("hello"))
	got, err := readWebSocketFrame(bufio.NewReader(bytes.NewReader(frame)))
	if err != nil {
		t.Fatal(err)
	}
	if got.Opcode != wsOpcodeText || string(got.Payload) != "hello" {
		t.Fatalf("frame = %#v", got)
	}
}

func TestEncodeWebSocketFrameHeaderUsesServerUnmaskedFrames(t *testing.T) {
	got := encodeWebSocketFrameHeader(wsOpcodeText, 5)
	if !bytes.Equal(got, []byte{0x81, 0x05}) {
		t.Fatalf("header = %#v", got)
	}
}

func TestWriteWebSocketCloseWithCodeIncludesReason(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	ws := &websocketConn{conn: server, reader: bufio.NewReader(server)}
	done := make(chan error, 1)
	go func() {
		done <- ws.WriteCloseWithCode(1003, websocketBinaryMessageCloseReason)
	}()

	frame, err := readWebSocketFrame(bufio.NewReader(client))
	if err != nil {
		t.Fatal(err)
	}
	if frame.Opcode != wsOpcodeClose || len(frame.Payload) < 2 {
		t.Fatalf("close frame = %#v", frame)
	}
	if code := binary.BigEndian.Uint16(frame.Payload[:2]); code != 1003 {
		t.Fatalf("close code = %d", code)
	}
	if reason := string(frame.Payload[2:]); reason != websocketBinaryMessageCloseReason {
		t.Fatalf("close reason = %q", reason)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func maskedClientFrame(opcode byte, payload []byte) []byte {
	mask := []byte{1, 2, 3, 4}
	out := []byte{0x80 | opcode, 0x80 | byte(len(payload))}
	out = append(out, mask...)
	for i, b := range payload {
		out = append(out, b^mask[i%4])
	}
	return out
}

func TestEncodeWebSocketFrameHeaderUsesExtendedLength(t *testing.T) {
	got := encodeWebSocketFrameHeader(wsOpcodeText, 126)
	want := []byte{0x81, 126, 0, 126}
	if !bytes.Equal(got, want) {
		t.Fatalf("header = %#v", got)
	}

	got = encodeWebSocketFrameHeader(wsOpcodeText, 65536)
	if len(got) != 10 || got[0] != 0x81 || got[1] != 127 || binary.BigEndian.Uint64(got[2:]) != 65536 {
		t.Fatalf("large header = %#v", got)
	}
}
