package gameserver

import (
	"bytes"
	"net"
	"testing"
)

func TestRCONRoundTrip(t *testing.T) {
	pkt := rconEncode(7, rconExecCommand, "ListPlayers")
	id, typ, body, err := rconDecode(bytes.NewReader(pkt))
	if err != nil || id != 7 || typ != rconExecCommand || body != "ListPlayers" {
		t.Fatalf("decode = %d %d %q %v", id, typ, body, err)
	}
}

// A fake server: checks the password, then echoes a player list.
func TestRCONAgainstFakeServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		id, _, pw, _ := rconDecode(c)
		if pw != "secret" {
			id = -1
		}
		c.Write(rconEncode(id, 0, ""))            // empty RESPONSE_VALUE quirk
		c.Write(rconEncode(id, rconAuthResp, "")) // AUTH_RESPONSE
		id, _, _, _ = rconDecode(c)
		c.Write(rconEncode(id, 0, "0. Alice, 0002abc\n1. Bob, 0002def\n"))
	}()

	out, err := RCON(ln.Addr().String(), "secret", "ListPlayers")
	if err != nil {
		t.Fatal(err)
	}
	if n := countARKPlayers(out); n != 2 {
		t.Fatalf("players = %d from %q", n, out)
	}
}

func TestCountARKPlayers(t *testing.T) {
	if n := countARKPlayers("No Players Connected"); n != 0 {
		t.Errorf("got %d", n)
	}
}
