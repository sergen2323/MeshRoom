package nat

import (
	"encoding/binary"
	"net"
	"testing"
)

func TestSTUNRoundTrip(t *testing.T) {
	req, txID := buildBindingRequest()
	if len(req) != 20 {
		t.Fatalf("request len = %d, want 20", len(req))
	}
	if binary.BigEndian.Uint16(req[0:]) != stunBindingRequest {
		t.Fatal("wrong message type")
	}
	if binary.BigEndian.Uint32(req[4:]) != stunMagicCookie {
		t.Fatal("wrong magic cookie")
	}

	// собираем успешный ответ с XOR-MAPPED-ADDRESS = 203.0.113.7:41234
	wantIP := net.IPv4(203, 0, 113, 7).To4()
	wantPort := uint16(41234)
	resp := make([]byte, 20+12)
	binary.BigEndian.PutUint16(resp[0:], stunBindingSuccess)
	binary.BigEndian.PutUint16(resp[2:], 12)
	binary.BigEndian.PutUint32(resp[4:], stunMagicCookie)
	copy(resp[8:], txID[:])
	binary.BigEndian.PutUint16(resp[20:], attrXORMappedAddr)
	binary.BigEndian.PutUint16(resp[22:], 8)
	resp[24] = 0
	resp[25] = 0x01 // IPv4
	binary.BigEndian.PutUint16(resp[26:], wantPort^uint16(stunMagicCookie>>16))
	cookie := resp[4:8]
	for i := 0; i < 4; i++ {
		resp[28+i] = wantIP[i] ^ cookie[i]
	}

	addr, err := parseBindingResponse(resp, txID)
	if err != nil {
		t.Fatal(err)
	}
	if !addr.IP.Equal(net.IP(wantIP)) || addr.Port != int(wantPort) {
		t.Fatalf("got %v, want %v:%d", addr, net.IP(wantIP), wantPort)
	}
}

func TestSTUNRejectsBadTxID(t *testing.T) {
	_, txID := buildBindingRequest()
	resp := make([]byte, 20)
	binary.BigEndian.PutUint16(resp[0:], stunBindingSuccess)
	binary.BigEndian.PutUint32(resp[4:], stunMagicCookie)
	// txID оставлен нулевым — не совпадёт
	if _, err := parseBindingResponse(resp, txID); err == nil {
		t.Fatal("expected transaction id mismatch error")
	}
}

func TestNATPMPResponse(t *testing.T) {
	resp := make([]byte, 16)
	resp[0] = natpmpVersion
	resp[1] = opMapUDP + opResultOffset
	binary.BigEndian.PutUint16(resp[2:], 0)       // result OK
	binary.BigEndian.PutUint16(resp[10:], 51820) // external port
	ext, err := parsePMPResponse(resp, opMapUDP)
	if err != nil || ext != 51820 {
		t.Fatalf("got %d, %v", ext, err)
	}

	// код ошибки → error
	binary.BigEndian.PutUint16(resp[2:], 2)
	if _, err := parsePMPResponse(resp, opMapUDP); err == nil {
		t.Fatal("expected error for non-zero result code")
	}
}

func TestSSDPLocationParse(t *testing.T) {
	resp := []byte("HTTP/1.1 200 OK\r\n" +
		"CACHE-CONTROL: max-age=120\r\n" +
		"LOCATION: http://192.168.1.1:5000/rootDesc.xml\r\n" +
		"ST: urn:schemas-upnp-org:device:InternetGatewayDevice:1\r\n\r\n")
	loc := parseSSDPForTest(resp)
	if loc != "http://192.168.1.1:5000/rootDesc.xml" {
		t.Fatalf("got %q", loc)
	}
}

func TestFindWANServiceAndResolveURL(t *testing.T) {
	xml := `<device><serviceList>
	  <service>
	    <serviceType>urn:schemas-upnp-org:service:WANIPConnection:1</serviceType>
	    <controlURL>/ctl/IPConn</controlURL>
	  </service>
	</serviceList></device>`
	st, ctrl := findWANService(xml)
	if st != "urn:schemas-upnp-org:service:WANIPConnection:1" || ctrl != "/ctl/IPConn" {
		t.Fatalf("got st=%q ctrl=%q", st, ctrl)
	}
	abs, err := resolveURL("http://192.168.1.1:5000/rootDesc.xml", ctrl)
	if err != nil || abs != "http://192.168.1.1:5000/ctl/IPConn" {
		t.Fatalf("got %q, %v", abs, err)
	}
}
