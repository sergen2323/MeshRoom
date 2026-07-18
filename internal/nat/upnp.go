package nat

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// UPnP-IGD: находим интернет-шлюз через SSDP (multicast) и просим его
// пробросить порт через SOAP-запрос AddPortMapping.

const ssdpAddr = "239.255.255.250:1900"

var igdSearchTargets = []string{
	"urn:schemas-upnp-org:device:InternetGatewayDevice:1",
	"urn:schemas-upnp-org:service:WANIPConnection:1",
	"urn:schemas-upnp-org:service:WANPPPConnection:1",
}

// igd — найденный шлюз: URL управления и тип сервиса.
type igd struct {
	controlURL  string
	serviceType string
}

// discoverIGD рассылает SSDP M-SEARCH и возвращает первый ответивший шлюз.
func discoverIGD(timeout time.Duration) (*igd, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	raddr, _ := net.ResolveUDPAddr("udp4", ssdpAddr)
	for _, st := range igdSearchTargets {
		msg := "M-SEARCH * HTTP/1.1\r\n" +
			"HOST: " + ssdpAddr + "\r\n" +
			"MAN: \"ssdp:discover\"\r\n" +
			"MX: 2\r\n" +
			"ST: " + st + "\r\n\r\n"
		_, _ = conn.WriteToUDP([]byte(msg), raddr)
	}

	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 2048)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			return nil, fmt.Errorf("upnp: no IGD found: %w", err)
		}
		loc := extractLocation(buf[:n])
		if loc == "" {
			continue
		}
		g, err := fetchIGDControl(loc)
		if err == nil {
			return g, nil
		}
	}
}

var locationRE = regexp.MustCompile(`(?i)location:\s*(\S+)`)

func extractLocation(resp []byte) string {
	m := locationRE.FindSubmatch(resp)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(string(m[1]))
}

// fetchIGDControl скачивает device-описание и находит control URL сервиса WAN.
func fetchIGDControl(descURL string) (*igd, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(descURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	svcType, ctrlPath := findWANService(string(body))
	if ctrlPath == "" {
		return nil, fmt.Errorf("upnp: no WAN service in description")
	}
	ctrlURL, err := resolveURL(descURL, ctrlPath)
	if err != nil {
		return nil, err
	}
	return &igd{controlURL: ctrlURL, serviceType: svcType}, nil
}

// findWANService вытаскивает serviceType и controlURL сервиса WANIP/WANPPP.
func findWANService(xml string) (serviceType, controlURL string) {
	for _, st := range []string{
		"urn:schemas-upnp-org:service:WANIPConnection:1",
		"urn:schemas-upnp-org:service:WANPPPConnection:1",
		"urn:schemas-upnp-org:service:WANIPConnection:2",
	} {
		idx := strings.Index(xml, st)
		if idx < 0 {
			continue
		}
		// ищем <controlURL> в том же <service>-блоке
		seg := xml[idx:]
		if end := strings.Index(seg, "</service>"); end > 0 {
			seg = seg[:end]
		}
		if cu := between(seg, "<controlURL>", "</controlURL>"); cu != "" {
			return st, strings.TrimSpace(cu)
		}
	}
	return "", ""
}

func between(s, a, b string) string {
	i := strings.Index(s, a)
	if i < 0 {
		return ""
	}
	i += len(a)
	j := strings.Index(s[i:], b)
	if j < 0 {
		return ""
	}
	return s[i : i+j]
}

// resolveURL превращает относительный controlURL в абсолютный по base.
func resolveURL(base, ref string) (string, error) {
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref, nil
	}
	// base вида http://192.168.1.1:5000/desc.xml → берём scheme+host
	rest := strings.TrimPrefix(base, "http://")
	host := rest
	if slash := strings.IndexByte(rest, '/'); slash >= 0 {
		host = rest[:slash]
	}
	if !strings.HasPrefix(ref, "/") {
		ref = "/" + ref
	}
	return "http://" + host + ref, nil
}

// UPnPMap пробрасывает порт через найденный IGD. Возвращает сам шлюз для
// последующего удаления/обновления маппинга.
func UPnPMap(p Proto, internalIP net.IP, internalPort, externalPort, lifetime int) (*igd, error) {
	g, err := discoverIGD(3 * time.Second)
	if err != nil {
		return nil, err
	}
	if err := g.addMapping(p, internalIP, internalPort, externalPort, lifetime); err != nil {
		return nil, err
	}
	return g, nil
}

func (g *igd) addMapping(p Proto, internalIP net.IP, internalPort, externalPort, lifetime int) error {
	proto := "UDP"
	if p == TCP {
		proto = "TCP"
	}
	body := fmt.Sprintf(`<u:AddPortMapping xmlns:u="%s">`+
		`<NewRemoteHost></NewRemoteHost>`+
		`<NewExternalPort>%d</NewExternalPort>`+
		`<NewProtocol>%s</NewProtocol>`+
		`<NewInternalPort>%d</NewInternalPort>`+
		`<NewInternalClient>%s</NewInternalClient>`+
		`<NewEnabled>1</NewEnabled>`+
		`<NewPortMappingDescription>MeshRoom</NewPortMappingDescription>`+
		`<NewLeaseDuration>%d</NewLeaseDuration>`+
		`</u:AddPortMapping>`,
		g.serviceType, externalPort, proto, internalPort, internalIP.String(), lifetime)
	_, err := g.soapCall("AddPortMapping", body)
	return err
}

// DeleteMapping снимает проброс (best-effort).
func (g *igd) DeleteMapping(p Proto, externalPort int) error {
	proto := "UDP"
	if p == TCP {
		proto = "TCP"
	}
	body := fmt.Sprintf(`<u:DeletePortMapping xmlns:u="%s">`+
		`<NewRemoteHost></NewRemoteHost>`+
		`<NewExternalPort>%d</NewExternalPort>`+
		`<NewProtocol>%s</NewProtocol>`+
		`</u:DeletePortMapping>`, g.serviceType, externalPort, proto)
	_, err := g.soapCall("DeletePortMapping", body)
	return err
}

func (g *igd) soapCall(action, innerXML string) ([]byte, error) {
	envelope := `<?xml version="1.0"?>` +
		`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" ` +
		`s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body>` +
		innerXML + `</s:Body></s:Envelope>`
	req, err := http.NewRequest("POST", g.controlURL, bytes.NewBufferString(envelope))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPAction", fmt.Sprintf(`"%s#%s"`, g.serviceType, action))
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		return body, fmt.Errorf("upnp: %s failed: HTTP %d", action, resp.StatusCode)
	}
	return body, nil
}

// parseSSDPForTest — экспортируемый хук для тестов дискавери.
func parseSSDPForTest(resp []byte) string { return extractLocation(resp) }
