package onvif

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/AlexxIT/go2rtc/internal/api/ws"
	"github.com/AlexxIT/go2rtc/pkg/onvif"
	"github.com/stretchr/testify/require"
)

// TestEventFanout verifies a subscribed WS client receives a device's events and
// that events for other devices are not delivered to it.
func TestEventFanout(t *testing.T) {
	dev, err := onvif.NewDevice(onvif.DeviceConfig{URL: "onvif://user:pass@127.0.0.1"})
	require.NoError(t, err)

	devicesMu.Lock()
	devices["cam"] = dev
	devicesMu.Unlock()
	t.Cleanup(func() {
		devicesMu.Lock()
		delete(devices, "cam")
		delete(sinks, "cam")
		devicesMu.Unlock()
	})

	var mu sync.Mutex
	var got []*ws.Message
	tr := &ws.Transport{Request: &http.Request{URL: &url.URL{RawQuery: "src=cam"}}}
	tr.OnWrite(func(msg any) error {
		mu.Lock()
		defer mu.Unlock()
		if m, ok := msg.(*ws.Message); ok {
			got = append(got, m)
		}
		return nil
	})

	require.NoError(t, handlerWSOnvif(tr, nil))

	broadcastEvent("other", onvif.Event{Topic: "ignored"}) // no subscriber -> dropped
	broadcastEvent("cam", onvif.Event{
		Topic: "tns1:VideoSource/MotionAlarm",
		Data:  map[string]string{"State": "true"},
	})

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, got, 2) // [0] subscribe ack, [1] event
	require.Equal(t, "onvif", got[1].Type)

	ev, ok := got[1].Value.(wsEvent)
	require.True(t, ok)
	require.Equal(t, "cam", ev.Device)
	require.Equal(t, "tns1:VideoSource/MotionAlarm", ev.Topic)
	require.Equal(t, "true", ev.Data["State"])
}

// TestEventFanoutRejectsUnknown verifies the WS handler rejects unknown devices.
func TestEventFanoutRejectsUnknown(t *testing.T) {
	tr := &ws.Transport{Request: &http.Request{URL: &url.URL{RawQuery: "src=missing"}}}
	tr.OnWrite(func(msg any) error { return nil })
	require.Error(t, handlerWSOnvif(tr, nil))
}

// a subscription minted on one device must not be pullable/renewable/cancellable
// through another device's emulated endpoint
func TestEmuSubscriptionDeviceScoping(t *testing.T) {
	id := emuSubscribe("camA")
	defer emuUnsubscribe("camA", id)

	if _, ok := emuPull("camB", id, 0); ok {
		t.Fatal("camB must not pull camA's subscription")
	}
	emuUnsubscribe("camB", id) // must be a no-op
	if _, ok := emuPull("camA", id, 0); !ok {
		t.Fatal("camA's subscription should still exist")
	}
}

func TestEventCompatibilityOperations(t *testing.T) {
	dev, err := onvif.NewDevice(onvif.DeviceConfig{URL: "onvif://127.0.0.1"})
	require.NoError(t, err)

	srv := httptest.NewServer(deviceONVIFHandler("camevent", dev))
	defer srv.Close()

	post := func(body string) string {
		t.Helper()
		resp, err := srv.Client().Post(
			srv.URL+"/onvif/event_service",
			"application/soap+xml",
			strings.NewReader(body),
		)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return string(b)
	}

	subscribe := post(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:wsnt="http://docs.oasis-open.org/wsn/b-2"><s:Body><wsnt:Subscribe/></s:Body></s:Envelope>`)
	require.Contains(t, subscribe, "<wsnt:SubscribeResponse>")
	require.Contains(t, subscribe, "/onvif/Subscription?Idx=")

	syncPoint := post(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:tev="http://www.onvif.org/ver10/events/wsdl"><s:Body><tev:SetSynchronizationPoint/></s:Body></s:Envelope>`)
	require.Contains(t, syncPoint, "<tev:SetSynchronizationPointResponse/>")
}
