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
