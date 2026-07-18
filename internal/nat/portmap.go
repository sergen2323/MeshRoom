package nat

import (
	"fmt"
	"log"
	"net"
	"sync"
	"time"
)

// PortMapper пытается пробросить порт наружу через NAT-PMP, затем UPnP,
// и держит маппинг живым, периодически обновляя аренду.
type PortMapper struct {
	proto        Proto
	internalPort int

	mu           sync.Mutex
	externalPort int
	externalIP   net.IP
	method       string // "natpmp" | "upnp" | ""
	igdRef       *igd
	stop         chan struct{}
	stopped      bool
}

// MapResult — итог попытки проброса.
type MapResult struct {
	Success      bool
	Method       string
	ExternalIP   net.IP
	ExternalPort int
	Err          error
}

const mapLifetime = 3600 // секунд

// NewPortMapper создаёт маппер для внутреннего порта.
func NewPortMapper(p Proto, internalPort int) *PortMapper {
	return &PortMapper{proto: p, internalPort: internalPort, stop: make(chan struct{})}
}

// Start выполняет первую попытку проброса и запускает фоновое обновление аренды.
func (pm *PortMapper) Start() MapResult {
	res := pm.tryMap()
	if res.Success {
		go pm.renewLoop()
	}
	return res
}

func (pm *PortMapper) tryMap() MapResult {
	gw, lanIP := DefaultGateway()
	if gw == nil {
		return MapResult{Err: fmt.Errorf("no default gateway found")}
	}

	// 1) NAT-PMP
	if ext, err := PMPMap(gw, pm.proto, pm.internalPort, mapLifetime); err == nil {
		extIP, _ := PMPExternalIP(gw)
		pm.set("natpmp", nil, ext, extIP)
		return MapResult{Success: true, Method: "natpmp", ExternalPort: ext, ExternalIP: extIP}
	}

	// 2) UPnP-IGD
	if lanIP != nil {
		if g, err := UPnPMap(pm.proto, lanIP, pm.internalPort, pm.internalPort, mapLifetime); err == nil {
			pm.set("upnp", g, pm.internalPort, nil)
			return MapResult{Success: true, Method: "upnp", ExternalPort: pm.internalPort}
		}
	}
	return MapResult{Err: fmt.Errorf("no UPnP/NAT-PMP gateway responded")}
}

func (pm *PortMapper) set(method string, g *igd, extPort int, extIP net.IP) {
	pm.mu.Lock()
	pm.method = method
	pm.igdRef = g
	pm.externalPort = extPort
	pm.externalIP = extIP
	pm.mu.Unlock()
}

// ExternalPort — внешний порт (0, если проброс не удался).
func (pm *PortMapper) ExternalPort() int {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.externalPort
}

func (pm *PortMapper) renewLoop() {
	t := time.NewTicker(time.Duration(mapLifetime/2) * time.Second)
	defer t.Stop()
	for {
		select {
		case <-pm.stop:
			return
		case <-t.C:
			if res := pm.tryMap(); !res.Success {
				log.Printf("nat: port mapping renew failed: %v", res.Err)
			}
		}
	}
}

// Close снимает проброс и останавливает обновление.
func (pm *PortMapper) Close() {
	pm.mu.Lock()
	if pm.stopped {
		pm.mu.Unlock()
		return
	}
	pm.stopped = true
	method := pm.method
	g := pm.igdRef
	ext := pm.externalPort
	pm.mu.Unlock()
	close(pm.stop)
	switch method {
	case "upnp":
		if g != nil {
			_ = g.DeleteMapping(pm.proto, ext)
		}
	case "natpmp":
		gw, _ := DefaultGateway()
		if gw != nil {
			_, _ = PMPMap(gw, pm.proto, pm.internalPort, 0) // lifetime 0 = удалить
		}
	}
}
