package gameserver

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"
)

// Minimal Source RCON client (https://developer.valvesoftware.com/wiki/Source_RCON_Protocol),
// used by games that expose RCON but not A2S (e.g. ARK: Survival Ascended).

const (
	rconAuth        = 3
	rconExecCommand = 2
	rconAuthResp    = 2
)

func rconEncode(id, typ int32, body string) []byte {
	var b bytes.Buffer
	size := int32(4 + 4 + len(body) + 2)
	_ = binary.Write(&b, binary.LittleEndian, size)
	_ = binary.Write(&b, binary.LittleEndian, id)
	_ = binary.Write(&b, binary.LittleEndian, typ)
	b.WriteString(body)
	b.Write([]byte{0, 0})
	return b.Bytes()
}

func rconDecode(r io.Reader) (id, typ int32, body string, err error) {
	var size int32
	if err = binary.Read(r, binary.LittleEndian, &size); err != nil {
		return
	}
	if size < 10 || size > 1<<20 {
		return 0, 0, "", fmt.Errorf("rcon: bad packet size %d", size)
	}
	buf := make([]byte, size)
	if _, err = io.ReadFull(r, buf); err != nil {
		return
	}
	id = int32(binary.LittleEndian.Uint32(buf[0:4]))
	typ = int32(binary.LittleEndian.Uint32(buf[4:8]))
	body = string(bytes.TrimRight(buf[8:], "\x00"))
	return
}

// RCON runs one command against addr with the given password and returns the
// response body.
func RCON(addr, password, command string) (string, error) {
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	if _, err := conn.Write(rconEncode(1, rconAuth, password)); err != nil {
		return "", err
	}
	// Servers may send an empty RESPONSE_VALUE before the AUTH_RESPONSE.
	for {
		id, typ, _, err := rconDecode(conn)
		if err != nil {
			return "", err
		}
		if typ == rconAuthResp {
			if id == -1 {
				return "", fmt.Errorf("rcon: authentication failed")
			}
			break
		}
	}

	if _, err := conn.Write(rconEncode(2, rconExecCommand, command)); err != nil {
		return "", err
	}
	_, _, body, err := rconDecode(conn)
	return body, err
}
