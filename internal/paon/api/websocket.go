package api

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/labstack/echo/v5"
)

const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

const (
	wsOpcodeContinuation = 0x0
	wsOpcodeText         = 0x1
	wsOpcodeBinary       = 0x2
	wsOpcodeClose        = 0x8
	wsOpcodePing         = 0x9
	wsOpcodePong         = 0xa
)

type websocketConn struct {
	conn   net.Conn
	reader *bufio.Reader
	mu     sync.Mutex
}

type websocketFrame struct {
	Opcode  byte
	Payload []byte
}

func isWebSocketRequest(c *echo.Context) bool {
	return strings.EqualFold(c.Request().Header.Get("Upgrade"), "websocket") &&
		strings.Contains(strings.ToLower(c.Request().Header.Get("Connection")), "upgrade")
}

func upgradeWebSocket(c *echo.Context) (*websocketConn, error) {
	key := c.Request().Header.Get("Sec-WebSocket-Key")
	if key == "" {
		return nil, errors.New("missing Sec-WebSocket-Key")
	}
	hijacker, ok := c.Response().(http.Hijacker)
	if !ok {
		return nil, errors.New("response writer does not support hijacking")
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return nil, err
	}
	response := websocketHandshakeResponse(key, c.Request().Header.Get("Sec-WebSocket-Protocol"))
	if _, err := rw.WriteString(response); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := rw.Flush(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &websocketConn{conn: conn, reader: rw.Reader}, nil
}

func websocketAcceptKey(key string) string {
	sum := sha1.Sum([]byte(key + websocketGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func websocketHandshakeResponse(key string, protocolHeader string) string {
	response := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + websocketAcceptKey(key) + "\r\n"
	if protocol := webSocketProtocolToken(protocolHeader); protocol != "" {
		response += "Sec-WebSocket-Protocol: " + protocol + "\r\n"
	}
	return response + "\r\n"
}

func (ws *websocketConn) Close() error {
	return ws.conn.Close()
}

func (ws *websocketConn) WriteText(payload []byte) error {
	return ws.writeFrame(wsOpcodeText, payload)
}

func (ws *websocketConn) WriteClose() error {
	_ = ws.writeFrame(wsOpcodeClose, nil)
	return ws.conn.Close()
}

func (ws *websocketConn) WriteCloseWithCode(code uint16, reason string) error {
	payload := make([]byte, 2, 2+len(reason))
	binary.BigEndian.PutUint16(payload, code)
	payload = append(payload, reason...)
	_ = ws.writeFrame(wsOpcodeClose, payload)
	return ws.conn.Close()
}

func (ws *websocketConn) WritePing(payload []byte) error {
	return ws.writeFrame(wsOpcodePing, payload)
}

func (ws *websocketConn) WritePong(payload []byte) error {
	return ws.writeFrame(wsOpcodePong, payload)
}

func (ws *websocketConn) writeFrame(opcode byte, payload []byte) error {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	header := encodeWebSocketFrameHeader(opcode, len(payload))
	if _, err := ws.conn.Write(header); err != nil {
		return err
	}
	if len(payload) > 0 {
		_, err := ws.conn.Write(payload)
		return err
	}
	return nil
}

func (ws *websocketConn) ReadFrame() (websocketFrame, error) {
	return readWebSocketFrame(ws.reader)
}

func readWebSocketFrame(reader *bufio.Reader) (websocketFrame, error) {
	first, err := reader.ReadByte()
	if err != nil {
		return websocketFrame{}, err
	}
	second, err := reader.ReadByte()
	if err != nil {
		return websocketFrame{}, err
	}
	opcode := first & 0x0f
	masked := second&0x80 != 0
	length := uint64(second & 0x7f)
	switch length {
	case 126:
		var buf [2]byte
		if _, err := io.ReadFull(reader, buf[:]); err != nil {
			return websocketFrame{}, err
		}
		length = uint64(binary.BigEndian.Uint16(buf[:]))
	case 127:
		var buf [8]byte
		if _, err := io.ReadFull(reader, buf[:]); err != nil {
			return websocketFrame{}, err
		}
		length = binary.BigEndian.Uint64(buf[:])
	}
	if length > 1<<20 {
		return websocketFrame{}, fmt.Errorf("websocket frame too large")
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(reader, mask[:]); err != nil {
			return websocketFrame{}, err
		}
	}
	payload := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(reader, payload); err != nil {
			return websocketFrame{}, err
		}
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return websocketFrame{Opcode: opcode, Payload: payload}, nil
}

func encodeWebSocketFrameHeader(opcode byte, length int) []byte {
	header := []byte{0x80 | opcode}
	switch {
	case length < 126:
		header = append(header, byte(length))
	case length <= 65535:
		header = append(header, 126, byte(length>>8), byte(length))
	default:
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], uint64(length))
		header = append(header, 127)
		header = append(header, buf[:]...)
	}
	return header
}
