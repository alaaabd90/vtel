package tunnel

import (
	"testing"
	"time"

	"github.com/alaaabd90/vtel/protocol"
)

func recvOrTimeout(t *testing.T, ch <-chan []byte, want string) {
	t.Helper()
	select {
	case data := <-ch:
		if string(data) != want {
			t.Fatalf("got %q, want %q", data, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %q", want)
	}
}

func TestDeliverOrderedBuffersOutOfOrderFrames(t *testing.T) {
	m := NewMux(func(f *protocol.Frame, urgent bool) {})
	c := m.RegisterConn(1)

	// Deliver Seq 2, then 1, then 0 - out of order. Only once Seq 0 arrives
	// should anything reach DataCh, and it must come out in order.
	m.HandleFrame(&protocol.Frame{Type: protocol.TypeData, ConnID: 1, SeqNum: 2, Payload: []byte("C")})
	m.HandleFrame(&protocol.Frame{Type: protocol.TypeData, ConnID: 1, SeqNum: 1, Payload: []byte("B")})
	m.HandleFrame(&protocol.Frame{Type: protocol.TypeData, ConnID: 1, SeqNum: 0, Payload: []byte("A")})

	recvOrTimeout(t, c.DataCh, "A")
	recvOrTimeout(t, c.DataCh, "B")
	recvOrTimeout(t, c.DataCh, "C")
}

func TestDeliverOrderedDropsDuplicateSeq(t *testing.T) {
	m := NewMux(func(f *protocol.Frame, urgent bool) {})
	c := m.RegisterConn(1)

	m.HandleFrame(&protocol.Frame{Type: protocol.TypeData, ConnID: 1, SeqNum: 0, Payload: []byte("A")})
	// A retried resend of the same batch redelivering Seq 0 must be dropped,
	// not re-applied or misinterpreted as Seq 1.
	m.HandleFrame(&protocol.Frame{Type: protocol.TypeData, ConnID: 1, SeqNum: 0, Payload: []byte("A-dup")})
	m.HandleFrame(&protocol.Frame{Type: protocol.TypeData, ConnID: 1, SeqNum: 1, Payload: []byte("B")})

	recvOrTimeout(t, c.DataCh, "A")
	recvOrTimeout(t, c.DataCh, "B")

	select {
	case data := <-c.DataCh:
		t.Fatalf("unexpected extra delivery after dedup: %q", data)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestRegisterConnBeforeDialBuffersMidDialData reproduces the fixed
// register-before-dial bug: a TypeData frame arriving for a connection ID
// that has been registered but not yet handed to Relay (i.e. the dial is
// still "in flight") must be buffered, not dropped. Before the fix,
// Server.handleConnect only called RegisterConn AFTER net.DialTimeout
// returned, so any TypeData racing ahead of a slow dial found no conn in
// m.conns and was silently discarded.
func TestRegisterConnBeforeDialBuffersMidDialData(t *testing.T) {
	m := NewMux(func(f *protocol.Frame, urgent bool) {})

	// RegisterConn happens first, exactly as Server.handleConnect now does
	// before calling net.DialTimeout.
	c := m.RegisterConn(42)

	// Simulate a frame racing ahead of a still-in-flight dial.
	m.HandleFrame(&protocol.Frame{Type: protocol.TypeData, ConnID: 42, SeqNum: 0, Payload: []byte("mid-dial")})

	// Only now does the simulated dial "complete" and something start
	// consuming DataCh (mirroring Relay's tunnel->net loop).
	recvOrTimeout(t, c.DataCh, "mid-dial")
}

func TestHandleFrameDataForUnregisteredConnIsDroppedNotPanicking(t *testing.T) {
	m := NewMux(func(f *protocol.Frame, urgent bool) {})
	// No RegisterConn/NewConn call for ID 99 - must be a silent no-op, not a panic.
	m.HandleFrame(&protocol.Frame{Type: protocol.TypeData, ConnID: 99, SeqNum: 0, Payload: []byte("dropped")})
}

func TestSendDataStampsIncrementingSeq(t *testing.T) {
	var got []*protocol.Frame
	m := NewMux(func(f *protocol.Frame, urgent bool) {
		got = append(got, f)
	})
	c := m.NewConn()

	m.SendData(c.ID, []byte("first"))
	m.SendData(c.ID, []byte("second"))

	if len(got) != 2 {
		t.Fatalf("got %d frames, want 2", len(got))
	}
	if got[0].SeqNum != 0 || got[1].SeqNum != 1 {
		t.Fatalf("SeqNum sequence = %d, %d; want 0, 1", got[0].SeqNum, got[1].SeqNum)
	}
}

func TestSendDataClassifiesUrgencyViaPriorityLatch(t *testing.T) {
	var urgencies []bool
	m := NewMux(func(f *protocol.Frame, urgent bool) {
		urgencies = append(urgencies, urgent)
	})
	c := m.NewConn()

	// First send, well under the threshold: urgent.
	m.SendData(c.ID, make([]byte, 1024))
	// Second send pushes cumulative bytes past the threshold: still urgent
	// (it's the frame that crosses the line), but the latch now flips.
	m.SendData(c.ID, make([]byte, protocol.StreamPriorityBytes))
	// Third send: latch is now demoted, so this one is normal.
	m.SendData(c.ID, make([]byte, 1))

	if len(urgencies) != 3 {
		t.Fatalf("got %d sends, want 3", len(urgencies))
	}
	if !urgencies[0] {
		t.Error("first send under threshold: want urgent=true")
	}
	if !urgencies[1] {
		t.Error("send that crosses the threshold: want urgent=true")
	}
	if urgencies[2] {
		t.Error("send after the threshold was crossed: want urgent=false")
	}
}

func TestControlFramesAreAlwaysUrgent(t *testing.T) {
	var urgencies []bool
	m := NewMux(func(f *protocol.Frame, urgent bool) {
		urgencies = append(urgencies, urgent)
	})
	c := m.NewConn()

	// Demote the conn's data priority latch first...
	m.SendData(c.ID, make([]byte, protocol.StreamPriorityBytes+1))
	m.SendData(c.ID, make([]byte, 1)) // now normal

	m.SendClose(c.ID)
	m.SendReset(c.ID)

	if len(urgencies) != 4 {
		t.Fatalf("got %d sends, want 4", len(urgencies))
	}
	if urgencies[1] {
		t.Fatal("expected the demoted TypeData send to be normal")
	}
	if !urgencies[len(urgencies)-2] || !urgencies[len(urgencies)-1] {
		t.Fatalf("SendClose/SendReset must always be urgent, got %v", urgencies)
	}
}

func TestTypeCloseAndTypeResetBothCloseConn(t *testing.T) {
	for _, typ := range []byte{protocol.TypeClose, protocol.TypeReset} {
		m := NewMux(func(f *protocol.Frame, urgent bool) {})
		c := m.RegisterConn(1)

		m.HandleFrame(&protocol.Frame{Type: typ, ConnID: 1})

		select {
		case <-c.CloseCh:
		case <-time.After(2 * time.Second):
			t.Fatalf("frame type 0x%02x did not close the conn", typ)
		}
	}
}
