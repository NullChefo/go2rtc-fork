package onvif

import (
	"testing"

	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/core"
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
