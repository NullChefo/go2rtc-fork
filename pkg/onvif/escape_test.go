package onvif

import (
	"bytes"
	"encoding/xml"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// wellFormed fails the test if b is not well-formed XML.
func wellFormed(t *testing.T, b []byte) {
	t.Helper()
	dec := xml.NewDecoder(bytes.NewReader(b))
	for {
		_, err := dec.Token()
		if err == io.EOF {
			break
		}
		require.NoError(t, err, "not well-formed XML:\n%s", b)
	}
}

func TestEscapeXML(t *testing.T) {
	require.Equal(t, "a&amp;b&lt;c&gt;d&quot;e&apos;f", escapeXML(`a&b<c>d"e'f`))
}

func TestEmulatedResponsesWellFormed(t *testing.T) {
	now := time.Now()

	// the headline review bug: literal &cache=1s made every snapshot response malformed
	snap := GetSnapshotUriResponse("http://host/api/frame.jpeg?src=cam&cache=1s")
	wellFormed(t, snap)
	require.Contains(t, string(snap), "&amp;cache=1s")

	// hostile values from camera events / config must not break the XML
	stream := GetStreamUriResponse(`rtsp://host:554/cam&"<x`)
	wellFormed(t, stream)

	wellFormed(t, DeviceCapabilities("h:1", "/onvif/cam&1", true))
	wellFormed(t, DeviceServices("h:1", "/onvif/cam&1", true))
	wellFormed(t, DeviceProfilesResponse([]ProfileInfo{{Token: `cam&1`, Width: 2560, Height: 1440, Codec: "H265"}, {Token: `b"<x`}}, true))

	// strict gSOAP clients (XMEye NVRs) require the schema-mandatory elements
	// and the audio configurations, or they reject the whole profile document
	prof := string(DeviceProfilesResponse([]ProfileInfo{{
		Token:            "cam",
		Codec:            "H264",
		Audio:            "AAC",
		FrameRateLimit:   12,
		EncodingInterval: 2,
		BitrateLimit:     1536,
	}}, false))
	for _, want := range []string{
		"<tt:UseCount>",                 // VideoSourceConfiguration UseCount
		"<tt:Multicast>",                // mandatory in encoder configurations
		"<tt:AudioSourceConfiguration",  // XM requires audio configs
		"<tt:AudioEncoderConfiguration", // ...
		"<tt:Encoding>AAC</tt:Encoding>",
		"<tt:FrameRateLimit>12</tt:FrameRateLimit>",
		"<tt:EncodingInterval>2</tt:EncodingInterval>",
		"<tt:BitrateLimit>1536</tt:BitrateLimit>",
		"<tt:SessionTimeout>",
	} {
		require.Contains(t, prof, want)
	}
	legacy := string(GetProfilesResponse([]string{"cam"}))
	require.Contains(t, legacy, "<tt:FrameRateLimit>20</tt:FrameRateLimit>")
	require.Contains(t, legacy, "<tt:EncodingInterval>1</tt:EncodingInterval>")
	require.Contains(t, legacy, "<tt:BitrateLimit>12000</tt:BitrateLimit>")
	fallback := string(DeviceProfilesResponse([]ProfileInfo{{Token: "fallback"}}, false))
	require.Contains(t, fallback, "<tt:FrameRateLimit>20</tt:FrameRateLimit>")
	require.Contains(t, fallback, "<tt:EncodingInterval>1</tt:EncodingInterval>")
	require.Contains(t, fallback, "<tt:BitrateLimit>12000</tt:BitrateLimit>")
	require.Contains(t, string(DeviceVideoSourcesResponse([]ProfileInfo{{Token: "cam", FrameRateLimit: 12}})), "<tt:Framerate>12.000000</tt:Framerate>")
	wellFormed(t, DeviceVideoSourcesResponse([]ProfileInfo{{Token: `cam&1`, Width: 2560, Height: 1440}}))
	wellFormed(t, DeviceVideoEncoderConfigurationsResponse([]ProfileInfo{{Token: `c`, Codec: "H264"}}))
	singleVEC := DeviceVideoEncoderConfigurationResponse(ProfileInfo{Token: `c`, BitrateLimit: 1536}, 0)
	wellFormed(t, singleVEC)
	require.Contains(t, string(singleVEC), "<tt:BitrateLimit>1536</tt:BitrateLimit>")
	wellFormed(t, GetVideoSourcesResponse([]string{`cam&1`}))
	wellFormed(t, GetEventPropertiesResponse())
	wellFormed(t, EventServiceCapabilitiesResponse())
	wellFormed(t, CreatePullPointSubscriptionResponse("http://h/onvif/cam&1/Subscription?Idx=1", now))
	wellFormed(t, SubscribeResponse("http://h/onvif/cam&1/Subscription?Idx=2", now))
	wellFormed(t, SetSynchronizationPointResponse())
	wellFormed(t, RenewResponse(now))
	wellFormed(t, UnsubscribeResponse())

	// camera-supplied event Topic / SimpleItem values with XML metacharacters
	ev := Event{
		Topic:  "tns1:Rule&Engine<x",
		Time:   `2026-01-01T00:00:00Z`,
		Source: map[string]string{"Token": `a&b`},
		Data:   map[string]string{"State": `true"&<`},
	}
	pm := PullMessagesResponse([]Event{ev}, now)
	wellFormed(t, pm) // the real guarantee: parses cleanly despite hostile values
	require.Contains(t, string(pm), "tns1:Rule&amp;Engine&lt;x")
	require.Contains(t, string(pm), `Value="true&quot;&amp;&lt;"`)
}
