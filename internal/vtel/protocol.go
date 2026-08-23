package vtel

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
)

// Wire format. A "batch" is what actually gets uploaded as one Telegram
// document: a magic-tagged, gzip-compressed sequence of frames. Batching
// (rather than one document per frame) matters a lot more here than it does
// for gdrive's Drive-backed mux: Telegram's flood limits are message-count
// based, so cramming many frames into one document is the whole game, not
// just a throughput nicety.
const (
	batchMagic   = "VTB1"
	dirClientToExit byte = 0
	dirExitToClient byte = 1

	frameOpen byte = 1
	frameData byte = 2
	frameFIN  byte = 3
	frameRST  byte = 4
)

type frame struct {
	Kind     byte
	StreamID uint64
	Seq      uint64
	Payload  []byte
}

// encodeBatch gzip-compresses a magic header + direction byte + frame count
// + each frame (kind, streamID, seq, len-prefixed payload).
func encodeBatch(dir byte, frames []frame) ([]byte, error) {
	var raw bytes.Buffer
	raw.WriteString(batchMagic)
	raw.WriteByte(dir)
	var countBuf [4]byte
	binary.BigEndian.PutUint32(countBuf[:], uint32(len(frames)))
	raw.Write(countBuf[:])
	for _, f := range frames {
		raw.WriteByte(f.Kind)
		var idBuf [8]byte
		binary.BigEndian.PutUint64(idBuf[:], f.StreamID)
		raw.Write(idBuf[:])
		binary.BigEndian.PutUint64(idBuf[:], f.Seq)
		raw.Write(idBuf[:])
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(f.Payload)))
		raw.Write(lenBuf[:])
		raw.Write(f.Payload)
	}

	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	if _, err := gz.Write(raw.Bytes()); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// decodeBatch is the inverse of encodeBatch. Returns (nil, 0, nil) with no
// error for data that gunzips fine but isn't a vtel batch (wrong magic) —
// callers should treat that as "ignore, not ours" rather than a hard error,
// since the shared channel could in principle carry other content.
func decodeBatch(data []byte) (frames []frame, dir byte, ok bool, err error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, 0, false, nil
	}
	defer gz.Close()
	raw, err := io.ReadAll(gz)
	if err != nil {
		return nil, 0, false, nil
	}
	if len(raw) < len(batchMagic)+1+4 || string(raw[:len(batchMagic)]) != batchMagic {
		return nil, 0, false, nil
	}
	pos := len(batchMagic)
	dir = raw[pos]
	pos++
	count := binary.BigEndian.Uint32(raw[pos : pos+4])
	pos += 4
	frames = make([]frame, 0, count)
	for i := uint32(0); i < count; i++ {
		if pos+1+8+8+4 > len(raw) {
			return nil, 0, false, errors.New("vtel: truncated batch")
		}
		f := frame{Kind: raw[pos]}
		pos++
		f.StreamID = binary.BigEndian.Uint64(raw[pos : pos+8])
		pos += 8
		f.Seq = binary.BigEndian.Uint64(raw[pos : pos+8])
		pos += 8
		plen := binary.BigEndian.Uint32(raw[pos : pos+4])
		pos += 4
		if pos+int(plen) > len(raw) {
			return nil, 0, false, errors.New("vtel: truncated frame payload")
		}
		f.Payload = append([]byte(nil), raw[pos:pos+int(plen)]...)
		pos += int(plen)
		frames = append(frames, f)
	}
	return frames, dir, true, nil
}

// encodeOpenPayload packs the dial target and any already-read initial bytes
// (so the first read off the SOCKS connection doesn't have to wait for a
// second round trip) into a frameOpen's Payload.
func encodeOpenPayload(target string, initial []byte) []byte {
	var buf bytes.Buffer
	var lenBuf [2]byte
	binary.BigEndian.PutUint16(lenBuf[:], uint16(len(target)))
	buf.Write(lenBuf[:])
	buf.WriteString(target)
	buf.Write(initial)
	return buf.Bytes()
}

func decodeOpenPayload(payload []byte) (target string, initial []byte, err error) {
	if len(payload) < 2 {
		return "", nil, errors.New("vtel: short open payload")
	}
	tlen := int(binary.BigEndian.Uint16(payload[:2]))
	if 2+tlen > len(payload) {
		return "", nil, errors.New("vtel: truncated open target")
	}
	target = string(payload[2 : 2+tlen])
	initial = payload[2+tlen:]
	return target, initial, nil
}

// randomStreamID returns a non-zero random stream identifier, unique enough
// that two concurrently open streams from the same client never collide.
func randomStreamID() (uint64, error) {
	var b [8]byte
	for {
		if _, err := rand.Read(b[:]); err != nil {
			return 0, err
		}
		id := binary.BigEndian.Uint64(b[:])
		if id != 0 {
			return id, nil
		}
	}
}
