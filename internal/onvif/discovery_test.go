package onvif

import (
	"bytes"
	"encoding/xml"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func xmlWellFormed(t *testing.T, b []byte) {
	t.Helper()
	dec := xml.NewDecoder(bytes.NewReader(b))
	for {
		_, err := dec.Token()
		if err == io.EOF {
			break
		}
		require.NoError(t, err, "not well-formed:\n%s", b)
	}
}

func TestProbeMatch(t *testing.T) {
	ep := onvifEndpoint{name: "balcony", listen: ":8901", uuid: "dev-uuid-1"}
	b := probeMatch(ep, "10.0.0.5", "urn:uuid:probe-1")

	xmlWellFormed(t, b)
	s := string(b)
	require.Contains(t, s, "<d:XAddrs>http://10.0.0.5:8901/onvif/device_service</d:XAddrs>")
	require.Contains(t, s, "urn:uuid:dev-uuid-1")          // device EndpointReference
	require.Contains(t, s, "<w:RelatesTo>urn:uuid:probe-1") // echoes the probe MessageID
	require.Contains(t, s, "ProbeMatches")
	require.Contains(t, s, "onvif://www.onvif.org/name/balcony")
	require.Contains(t, s, "dn:NetworkVideoTransmitter")
}

func TestHelloMatch(t *testing.T) {
	b := helloMatch(onvifEndpoint{name: "cam", listen: ":9000", uuid: "u1"}, "1.2.3.4")
	xmlWellFormed(t, b)
	require.Contains(t, string(b), "<d:XAddrs>http://1.2.3.4:9000/onvif/device_service</d:XAddrs>")
	require.Contains(t, string(b), "discovery/Hello")
}

// hostile device name and probe MessageID must not break the discovery XML
func TestProbeMatchEscaping(t *testing.T) {
	ep := onvifEndpoint{name: `cam&1<x"y`, listen: ":8901", uuid: "u1"}
	b := probeMatch(ep, "10.0.0.5", `urn:uuid:a&b`)
	xmlWellFormed(t, b)
	xmlWellFormed(t, helloMatch(ep, "10.0.0.5"))

	// empty MessageID -> no RelatesTo element at all
	require.NotContains(t, string(probeMatch(ep, "10.0.0.5", "")), "RelatesTo")
}
