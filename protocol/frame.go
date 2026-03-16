package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	Version    = 1
	HeaderSize = 13 // version(1) + type(1) + connID(4) + seqNum(4) + payloadLen(3)

	TypeConnect    = 0x01
	TypeConnectACK = 0x02
	TypeData       = 0x03
	TypeClose      = 0x04
	TypeKeepalive  = 0x05
)

// Frame represents a single protocol frame.
// [version:1][type:1][connID:4][seqNum:4][payloadLen:3][payload:N]
type Frame struct {
	Type    byte
	ConnID  uint32
	SeqNum  uint32
	Payload []byte
}

// ConnectPayload is the payload of a CONNECT frame: 1-byte addrType + addr + 2-byte port
type ConnectPayload struct {
	AddrType byte   // 0x01=IPv4, 0x03=domain, 0x04=IPv6
	Addr     []byte // 4 bytes (IPv4), N bytes (domain), 16 bytes (IPv6)
	Port     uint16
}

func (cp *ConnectPayload) Marshal() []byte {
	// addrType(1) + addr + port(2)
	var buf []byte
	switch cp.AddrType {
	case 0x01: // IPv4
		buf = make([]byte, 1+4+2)
		buf[0] = cp.AddrType
		copy(buf[1:5], cp.Addr)
		binary.BigEndian.PutUint16(buf[5:7], cp.Port)
	case 0x03: // Domain
		buf = make([]byte, 1+1+len(cp.Addr)+2)
		buf[0] = cp.AddrType
		buf[1] = byte(len(cp.Addr))
		copy(buf[2:2+len(cp.Addr)], cp.Addr)
		binary.BigEndian.PutUint16(buf[2+len(cp.Addr):], cp.Port)
	case 0x04: // IPv6
		buf = make([]byte, 1+16+2)
		buf[0] = cp.AddrType
		copy(buf[1:17], cp.Addr)
		binary.BigEndian.PutUint16(buf[17:19], cp.Port)
	}
	return buf
}

func ParseConnectPayload(data []byte) (*ConnectPayload, error) {
	if len(data) < 1 {
		return nil, errors.New("connect payload too short")
	}
	cp := &ConnectPayload{AddrType: data[0]}
	switch cp.AddrType {
	case 0x01:
		if len(data) < 7 {
			return nil, errors.New("IPv4 connect payload too short")
		}
		cp.Addr = data[1:5]
		cp.Port = binary.BigEndian.Uint16(data[5:7])
	case 0x03:
		if len(data) < 2 {
			return nil, errors.New("domain connect payload too short")
		}
		dlen := int(data[1])
		if len(data) < 2+dlen+2 {
			return nil, errors.New("domain connect payload too short")
		}
		cp.Addr = data[2 : 2+dlen]
		cp.Port = binary.BigEndian.Uint16(data[2+dlen : 2+dlen+2])
	case 0x04:
		if len(data) < 19 {
			return nil, errors.New("IPv6 connect payload too short")
		}
		cp.Addr = data[1:17]
		cp.Port = binary.BigEndian.Uint16(data[17:19])
	default:
		return nil, fmt.Errorf("unknown address type: 0x%02x", cp.AddrType)
	}
	return cp, nil
}

func (cp *ConnectPayload) String() string {
	switch cp.AddrType {
	case 0x01:
		return fmt.Sprintf("%d.%d.%d.%d:%d", cp.Addr[0], cp.Addr[1], cp.Addr[2], cp.Addr[3], cp.Port)
	case 0x03:
		return fmt.Sprintf("%s:%d", string(cp.Addr), cp.Port)
	case 0x04:
		return fmt.Sprintf("[%x]:%d", cp.Addr, cp.Port)
	}
	return "unknown"
}

// Marshal serializes a frame to wire format.
func (f *Frame) Marshal() []byte {
	plen := len(f.Payload)
	buf := make([]byte, HeaderSize+plen)
	buf[0] = Version
	buf[1] = f.Type
	binary.BigEndian.PutUint32(buf[2:6], f.ConnID)
	binary.BigEndian.PutUint32(buf[6:10], f.SeqNum)
	// 3-byte payload length (big-endian)
	buf[10] = byte(plen >> 16)
	buf[11] = byte(plen >> 8)
	buf[12] = byte(plen)
	copy(buf[HeaderSize:], f.Payload)
	return buf
}

// ReadFrame reads a single frame from a reader.
func ReadFrame(r io.Reader) (*Frame, error) {
	hdr := make([]byte, HeaderSize)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, err
	}
	if hdr[0] != Version {
		return nil, fmt.Errorf("unsupported version: %d", hdr[0])
	}
	f := &Frame{
		Type:   hdr[1],
		ConnID: binary.BigEndian.Uint32(hdr[2:6]),
		SeqNum: binary.BigEndian.Uint32(hdr[6:10]),
	}
	plen := int(hdr[10])<<16 | int(hdr[11])<<8 | int(hdr[12])
	if plen > 0 {
		f.Payload = make([]byte, plen)
		if _, err := io.ReadFull(r, f.Payload); err != nil {
			return nil, err
		}
	}
	return f, nil
}
