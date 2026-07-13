package onvif

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/core"
	ponvif "github.com/AlexxIT/go2rtc/pkg/onvif"
	"github.com/stretchr/testify/require"
)

// TestStreamBelongsToDevice verifies the discrimination that prevents an
// auto-registered profile from silently aliasing onto an unrelated stream that
// merely shares its name.
func TestStreamBelongsToDevice(t *testing.T) {
	// register the schemes so streams.New accepts these sources
	// (the real modules' Init() does this in the app)
	noop := func(string) (core.Producer, error) { return nil, nil }
	streams.HandleFunc("onvif", noop)
	streams.HandleFunc("rtsp", noop)

	own, err := streams.New("onvif_own_test", "onvif://10.0.0.5:80?profile=Tok")
	require.NoError(t, err)
	t.Cleanup(func() { streams.Delete("onvif_own_test") })

	require.True(t, streamBelongsToDevice(own, "10.0.0.5:80"))  // same onvif host
	require.False(t, streamBelongsToDevice(own, "10.0.0.9:80")) // different host

	foreign, err := streams.New("onvif_foreign_test", "rtsp://10.0.0.5:554/x")
	require.NoError(t, err)
	t.Cleanup(func() { streams.Delete("onvif_foreign_test") })

	require.False(t, streamBelongsToDevice(foreign, "10.0.0.5:80")) // not an onvif source
}

func TestMirroredProfilesResponse(t *testing.T) {
	var host string
	rawProfiles := ponvif.GetProfilesResponse([]string{"000", "001"})

	handler := func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var resp []byte
		switch ponvif.GetRequestAction(b) {
		case ponvif.DeviceGetCapabilities:
			resp = ponvif.GetCapabilitiesResponse(host)
		case ponvif.DeviceGetDeviceInformation:
			resp = ponvif.GetDeviceInformationResponse("H264", "IPC", "1.0", "SN")
		case ponvif.MediaGetProfiles:
			resp = rawProfiles
		default:
			http.Error(w, "unsupported", http.StatusBadRequest)
			return
		}
		_, _ = w.Write(resp)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/onvif/device_service", handler)
	mux.HandleFunc("/onvif/media_service", handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	host = strings.TrimPrefix(srv.URL, "http://")

	dev, err := ponvif.NewDevice(ponvif.DeviceConfig{
		URL:            "onvif://admin:pass@" + host,
		MirrorProfiles: true,
	})
	require.NoError(t, err)

	infos := []ponvif.ProfileInfo{{Token: "000"}, {Token: "001"}}
	got, ok, err := mirroredProfilesResponse(dev, infos)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, rawProfiles, got)

	// Never expose a raw profile without a corresponding go2rtc stream.
	_, ok, err = mirroredProfilesResponse(dev, infos[:1])
	require.NoError(t, err)
	require.False(t, ok)

	disabled, err := ponvif.NewDevice(ponvif.DeviceConfig{URL: "onvif://" + host})
	require.NoError(t, err)
	_, ok, err = mirroredProfilesResponse(disabled, infos)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestEmulatedSOAPResponseHasContentLength(t *testing.T) {
	devicesMu.Lock()
	deviceStreams["camlen"] = []profileStream{{token: "000", stream: "camlen"}}
	devicesMu.Unlock()
	t.Cleanup(func() {
		devicesMu.Lock()
		delete(deviceStreams, "camlen")
		devicesMu.Unlock()
	})

	dev, err := ponvif.NewDevice(ponvif.DeviceConfig{URL: "onvif://127.0.0.1"})
	require.NoError(t, err)

	srv := httptest.NewServer(deviceONVIFHandler("camlen", dev))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/onvif/media_service", strings.NewReader(
		`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:trt="http://www.onvif.org/ver10/media/wsdl"><s:Body><trt:GetProfiles/></s:Body></s:Envelope>`,
	))
	require.NoError(t, err)
	req.Close = true // XM_ONVIF Filter sends Connection: close

	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NotEmpty(t, body)
	require.Equal(t, int64(len(body)), resp.ContentLength)
	require.Empty(t, resp.TransferEncoding)
	require.True(t, resp.Close)
}

// TestExposeCameraToken verifies the emulated device advertises the camera's own
// profile token (e.g. "000"), like a real XM camera, and that GetStreamUri's
// token->stream mapping resolves it back to the go2rtc stream name. XMEye NVRs
// map their channel to numeric main/sub tokens and won't stream otherwise.
func TestExposeCameraToken(t *testing.T) {
	devicesMu.Lock()
	deviceStreams["camx"] = []profileStream{
		{token: "000", stream: "balcony", width: 2560, height: 1440, codec: "H264"},
		{token: "001", stream: "balcony_1", width: 704, height: 576, codec: "H264"},
	}
	devicesMu.Unlock()
	t.Cleanup(func() { devicesMu.Lock(); delete(deviceStreams, "camx"); devicesMu.Unlock() })

	infos := profileInfos("camx")
	require.Len(t, infos, 2)
	require.Equal(t, "000", infos[0].Token) // camera token, not "balcony"
	require.Equal(t, "001", infos[1].Token)

	// GetStreamUri/GetSnapshotUri resolve the advertised token back to the stream
	require.Equal(t, "balcony", tokenToStream("camx", "000"))
	require.Equal(t, "balcony_1", tokenToStream("camx", "001"))
	// a stream name still resolves (pre-token clients), unknown falls through
	require.Equal(t, "balcony", tokenToStream("camx", "balcony"))
	require.Equal(t, "zzz", tokenToStream("camx", "zzz"))
}

// TestEmulatedIdentity verifies XM/XiongMai device identities are neutralized
// (so XMEye NVRs use ONVIF RTSP, not the native protocol) while other cameras
// keep their real manufacturer/model.
func TestEmulatedIdentity(t *testing.T) {
	// the real XM camera in this project
	m, mod := emulatedIdentity("H264", "IPC_NT98566_IPG-N4C-WQ2_S38")
	require.Equal(t, "go2rtc", m)
	require.Equal(t, "go2rtc", mod)

	require.True(t, isXiongMaiIdentity("H264", "IPC_NT98566_IPG-N4C-WQ2_S38"))
	require.True(t, isXiongMaiIdentity("XiongMai", "anything"))

	// a non-XM camera keeps its real identity
	require.False(t, isXiongMaiIdentity("Hikvision", "DS-2CD2032"))
	m, mod = emulatedIdentity("Hikvision", "DS-2CD2032")
	require.Equal(t, "Hikvision", m)
	require.Equal(t, "DS-2CD2032", mod)
}

func TestRemapVideoSourceToken(t *testing.T) {
	devicesMu.Lock()
	deviceStreams["camv"] = []profileStream{{token: "000", stream: "camv", vsToken: "RealVS0"}}
	devicesMu.Unlock()
	t.Cleanup(func() { devicesMu.Lock(); delete(deviceStreams, "camv"); devicesMu.Unlock() })

	in := `<timg:GetImagingSettings><timg:VideoSourceToken>camv</timg:VideoSourceToken></timg:GetImagingSettings>`
	out := remapVideoSourceToken("camv", in)
	if out != `<timg:GetImagingSettings><timg:VideoSourceToken>RealVS0</timg:VideoSourceToken></timg:GetImagingSettings>` {
		t.Fatalf("bad remap: %s", out)
	}
	// unknown token passes through untouched
	if remapVideoSourceToken("camv", `<timg:VideoSourceToken>Other</timg:VideoSourceToken>`) != `<timg:VideoSourceToken>Other</timg:VideoSourceToken>` {
		t.Fatal("unknown token must pass through")
	}
	// the emulated shared source token maps to the camera's real one
	if remapVideoSourceToken("camv", `<timg:VideoSourceToken>V_SRC_000</timg:VideoSourceToken>`) != `<timg:VideoSourceToken>RealVS0</timg:VideoSourceToken>` {
		t.Fatal("V_SRC_000 must map to the device's real source token")
	}
}
