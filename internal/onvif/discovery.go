package onvif

import (
	"errors"
	"net"
	"strings"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/onvif"
)

const wsDiscoveryAddr = "239.255.255.250:3702"

// onvifEndpoint is a virtual ONVIF device that go2rtc announces and serves on a
// dedicated port.
type onvifEndpoint struct {
	name   string
	listen string // e.g. ":8901"
	uuid   string // stable (per process) device UUID
}

var endpoints []onvifEndpoint

// startDiscovery answers WS-Discovery Probe requests (and sends a Hello burst)
// so ONVIF clients/NVRs auto-find each virtual camera on the LAN, exactly like a
// real ONVIF camera.
func startDiscovery(eps []onvifEndpoint) {
	addr, err := net.ResolveUDPAddr("udp4", wsDiscoveryAddr)
	if err != nil {
		log.Error().Err(err).Msg("[onvif] discovery resolve")
		return
	}

	conn, err := net.ListenMulticastUDP("udp4", nil, addr)
	if err != nil {
		log.Warn().Err(err).Msg("[onvif] discovery disabled (cannot bind :3702)")
		return
	}
	_ = conn.SetReadBuffer(1 << 16)

	log.Info().Msgf("[onvif] WS-Discovery responder for %d device(s)", len(eps))

	sendHello(eps)

	buf := make([]byte, 1<<16)
	for {
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return // socket closed: stop
			}
			// keep serving on any other (transient) read error
			log.Debug().Err(err).Msg("[onvif] discovery read")
			time.Sleep(time.Second)
			continue
		}

		// only reply to Probe (ignore our own Hello echo and ProbeMatches)
		if !strings.Contains(string(buf[:n]), "Probe") || strings.Contains(string(buf[:n]), "ProbeMatch") {
			continue
		}

		ip := localIPFor(src)
		if ip == "" {
			log.Debug().Msgf("[onvif] discovery: no local IP toward %s, skipping reply", src)
			continue
		}

		relatesTo := onvif.FindTagValue(buf[:n], "MessageID")

		for _, ep := range eps {
			if _, err = conn.WriteToUDP(probeMatch(ep, ip, relatesTo), src); err != nil {
				log.Debug().Err(err).Msg("[onvif] discovery reply")
			}
		}
		log.Debug().Msgf("[onvif] discovery: answered probe from %s with %d device(s) (xaddr ip=%s)", src, len(eps), ip)
	}
}

func sendHello(eps []onvifEndpoint) {
	conn, err := net.Dial("udp4", wsDiscoveryAddr)
	if err != nil {
		return
	}
	defer conn.Close()

	ip := ""
	if a, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		ip = a.IP.String()
	}
	for _, ep := range eps {
		_, _ = conn.Write(helloMatch(ep, ip))
	}
}

// localIPFor returns the local IP go2rtc would use to reach remote, so the
// advertised XAddrs point at an address the client can actually connect to.
func localIPFor(remote *net.UDPAddr) string {
	c, err := net.Dial("udp", remote.String())
	if err != nil {
		return ""
	}
	defer c.Close()
	if a, ok := c.LocalAddr().(*net.UDPAddr); ok {
		return a.IP.String()
	}
	return ""
}

func xaddr(ep onvifEndpoint, ip string) string {
	_, port, _ := net.SplitHostPort(ep.listen)
	return "http://" + ip + ":" + port + "/onvif/device_service"
}

func scopes(ep onvifEndpoint) string {
	return "onvif://www.onvif.org/type/Network_Video_Transmitter " +
		"onvif://www.onvif.org/Profile/Streaming " +
		"onvif://www.onvif.org/name/" + onvif.EscapeXML(ep.name) + " " +
		"onvif://www.onvif.org/hardware/go2rtc"
}

const wsdHeader = `<?xml version="1.0" encoding="UTF-8"?>` +
	`<e:Envelope xmlns:e="http://www.w3.org/2003/05/soap-envelope" xmlns:w="http://schemas.xmlsoap.org/ws/2004/08/addressing" xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery" xmlns:dn="http://www.onvif.org/ver10/network/wsdl">`

func probeMatch(ep onvifEndpoint, ip, relatesTo string) []byte {
	var rt string
	if relatesTo != "" {
		rt = `<w:RelatesTo>` + onvif.EscapeXML(relatesTo) + `</w:RelatesTo>`
	}
	return []byte(wsdHeader +
		`<e:Header>` +
		`<w:MessageID>urn:uuid:` + onvif.UUID() + `</w:MessageID>` +
		rt +
		`<w:To>http://schemas.xmlsoap.org/ws/2004/08/addressing/role/anonymous</w:To>` +
		`<w:Action>http://schemas.xmlsoap.org/ws/2005/04/discovery/ProbeMatches</w:Action>` +
		`</e:Header>` +
		`<e:Body><d:ProbeMatches><d:ProbeMatch>` +
		`<w:EndpointReference><w:Address>urn:uuid:` + ep.uuid + `</w:Address></w:EndpointReference>` +
		`<d:Types>dn:NetworkVideoTransmitter</d:Types>` +
		`<d:Scopes>` + scopes(ep) + `</d:Scopes>` +
		`<d:XAddrs>` + xaddr(ep, ip) + `</d:XAddrs>` +
		`<d:MetadataVersion>1</d:MetadataVersion>` +
		`</d:ProbeMatch></d:ProbeMatches></e:Body></e:Envelope>`)
}

func helloMatch(ep onvifEndpoint, ip string) []byte {
	return []byte(wsdHeader +
		`<e:Header>` +
		`<w:MessageID>urn:uuid:` + onvif.UUID() + `</w:MessageID>` +
		`<w:To>urn:schemas-xmlsoap-org:ws:2005:04:discovery</w:To>` +
		`<w:Action>http://schemas.xmlsoap.org/ws/2005/04/discovery/Hello</w:Action>` +
		`</e:Header>` +
		`<e:Body><d:Hello>` +
		`<w:EndpointReference><w:Address>urn:uuid:` + ep.uuid + `</w:Address></w:EndpointReference>` +
		`<d:Types>dn:NetworkVideoTransmitter</d:Types>` +
		`<d:Scopes>` + scopes(ep) + `</d:Scopes>` +
		`<d:XAddrs>` + xaddr(ep, ip) + `</d:XAddrs>` +
		`<d:MetadataVersion>1</d:MetadataVersion>` +
		`</d:Hello></e:Body></e:Envelope>`)
}
