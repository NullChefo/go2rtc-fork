package onvif

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDeviceSession spins up a fake ONVIF camera (reusing the server response
// builders) and verifies the persistent session resolves a stream URI end to
// end, including credential injection.
func TestDeviceSession(t *testing.T) {
	var host string // filled once the test server is listening

	handler := func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var resp []byte
		switch GetRequestAction(b) {
		case DeviceGetCapabilities:
			resp = GetCapabilitiesResponse(host)
		case DeviceGetDeviceInformation:
			resp = GetDeviceInformationResponse("ACME", "CamX", "1.0", "SN123")
		case MediaGetProfiles:
			resp = GetProfilesResponse([]string{"Profile_1", "Profile_2"})
		case MediaGetStreamUri:
			resp = GetStreamUriResponse("rtsp://" + host + "/stream/" + FindTagValue(b, "ProfileToken"))
		case MediaGetSnapshotUri:
			resp = GetSnapshotUriResponse("http://" + host + "/snap/" + FindTagValue(b, "ProfileToken"))
		default:
			http.Error(w, "unsupported", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
		_, _ = w.Write(resp)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/onvif/device_service", handler)
	mux.HandleFunc("/onvif/media_service", handler)
	mux.HandleFunc("/snap/", func(w http.ResponseWriter, r *http.Request) {
		// require Basic auth, then return fake JPEG bytes
		if u, p, ok := r.BasicAuth(); !ok || u != "admin" || p != "pass" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte{0xFF, 0xD8, 0xFF, 0xE0}) // JPEG SOI marker
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()
	host = strings.TrimPrefix(srv.URL, "http://")

	dev, err := NewDevice(DeviceConfig{URL: "onvif://admin:pass@" + host})
	require.NoError(t, err)
	require.NoError(t, dev.Connect())

	profiles, err := dev.GetProfiles()
	require.NoError(t, err)
	require.Len(t, profiles, 2)
	require.Equal(t, "Profile_1", profiles[0].Token)

	// resolve via the unambiguous profile token; expect creds injected
	uri, err := dev.ResolveURI(url.Values{"profile": {"Profile_2"}})
	require.NoError(t, err)
	require.Equal(t, "rtsp://admin:pass@"+host+"/stream/Profile_2", uri)

	// profile allowlist
	require.True(t, dev.ProfileAllowed("Profile_1"))
	allow, _ := NewDevice(DeviceConfig{URL: "onvif://h", Profiles: []string{"Profile_1"}})
	require.True(t, allow.ProfileAllowed("Profile_1"))
	require.False(t, allow.ProfileAllowed("Profile_2"))

	// native snapshot proxy (default mode is "stream")
	require.Equal(t, "stream", dev.SnapshotMode())
	img, contentType, err := dev.Snapshot("Profile_1")
	require.NoError(t, err)
	require.Equal(t, "image/jpeg", contentType)
	require.Equal(t, []byte{0xFF, 0xD8, 0xFF, 0xE0}, img)
}
