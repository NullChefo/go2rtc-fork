package onvif

import (
	"maps"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/onvif"
)

var devices = map[string]*onvif.Device{}
var devicesByHost = map[string]*onvif.Device{}

// deviceStreams maps a device name to its profiles in registration order
// (first = main), recording the go2rtc stream name each profile was exposed as.
var deviceStreams = map[string][]profileStream{}

var devicesMu sync.Mutex

type profileStream struct {
	token   string // camera profile token
	stream  string // exposed go2rtc stream name (what consumers/ONVIF use)
	width   int    // advertised resolution (real, from the camera)
	height  int
	codec   string // effective ONVIF codec the consumer receives (after transcode)
	audio   string // effective ONVIF audio encoding (G711 / AAC)
	vsToken string // camera VideoSource token (for imaging proxying)
}

// profileInfos builds the ONVIF emulation profile metadata for a device.
func profileInfos(device string) []onvif.ProfileInfo {
	devicesMu.Lock()
	defer devicesMu.Unlock()
	list := deviceStreams[device]
	infos := make([]onvif.ProfileInfo, 0, len(list))
	for _, ps := range list {
		// expose the CAMERA's own profile token (e.g. "000"/"001"): XMEye NVRs
		// derive their channel/stream mapping from the token format and never
		// request a stream for non-numeric tokens. GetStreamUri/GetSnapshotUri
		// translate the token back to the go2rtc stream name.
		infos = append(infos, onvif.ProfileInfo{Token: ps.token, Width: ps.width, Height: ps.height, Codec: ps.codec, Audio: ps.audio})
	}
	return infos
}

// profileInfo looks up one exposed profile by its token (the camera's own
// profile token); falls back to a defaults-only ProfileInfo if unknown.
func profileInfo(device, token string) onvif.ProfileInfo {
	for _, info := range profileInfos(device) {
		if info.Token == token {
			return info
		}
	}
	return onvif.ProfileInfo{Token: token}
}

// initDevices builds a persistent session for every camera in the onvif.devices
// config section and, once connected, auto-registers a go2rtc stream per
// profile. Heavy work is deferred (like streams.Init) so rtsp.Port and all
// source handlers are ready first.
func initDevices(configs map[string]onvif.DeviceConfig) {
	if len(configs) == 0 {
		return
	}

	for name, dc := range configs {
		if dc.URL == "" {
			log.Warn().Msgf("[onvif] device %q has no url", name)
			continue
		}

		dev, err := onvif.NewDevice(dc)
		if err != nil {
			log.Error().Err(err).Msgf("[onvif] device %q", name)
			continue
		}

		devicesMu.Lock()
		devices[name] = dev
		devicesByHost[dev.Host()] = dev
		devicesMu.Unlock()

		// expose this camera as its own virtual ONVIF device on a dedicated
		// port so consumers can use onvif://user:pass@go2rtc:<port>. Bind
		// synchronously so a port conflict (e.g. two cameras with the same
		// listen) is surfaced and we only announce listeners that are actually
		// live (no phantom WS-Discovery entry pointing at the wrong camera).
		if dc.Listen != "" {
			if err = startDeviceListener(name, dev, dc.Listen); err != nil {
				log.Error().Err(err).Msgf("[onvif] device %q listener %s", name, dc.Listen)
			} else {
				endpoints = append(endpoints, onvifEndpoint{name: name, listen: dc.Listen, uuid: onvif.UUID()})
			}
		}
	}

	// announce the virtual cameras via WS-Discovery (UDP multicast)
	if len(endpoints) > 0 {
		go startDiscovery(endpoints)
	}

	time.AfterFunc(time.Second, func() {
		devicesMu.Lock()
		list := make(map[string]*onvif.Device, len(devices))
		maps.Copy(list, devices)
		devicesMu.Unlock()

		for name, dev := range list {
			go connectAndRegister(name, dev)
		}
	})
}

// getDeviceByHost maps an onvif:// source host back to its configured device so
// streamOnvif can reuse the persistent session.
func getDeviceByHost(host string) *onvif.Device {
	devicesMu.Lock()
	defer devicesMu.Unlock()
	return devicesByHost[host]
}

// getDeviceByName returns a configured device by its config key.
func getDeviceByName(name string) *onvif.Device {
	devicesMu.Lock()
	defer devicesMu.Unlock()
	return devices[name]
}

// resolveProfileStream maps a device name + optional profile token to the
// registered go2rtc stream name and the resolved profile token. An empty token
// selects the main (first) profile. ok is false until the device has connected
// and registered its profiles.
func resolveProfileStream(device, token string) (stream, profileToken string, ok bool) {
	devicesMu.Lock()
	defer devicesMu.Unlock()

	list := deviceStreams[device]
	if len(list) == 0 {
		return "", "", false
	}
	if token == "" {
		return list[0].stream, list[0].token, true
	}
	for _, ps := range list {
		if ps.token == token {
			return ps.stream, ps.token, true
		}
	}
	return "", "", false
}

// connectAndRegister keeps trying to reach the camera (capped backoff) and, once
// reachable, registers its profiles as streams. Ongoing stream connect/reconnect
// is then handled by the shared producer.
func connectAndRegister(name string, dev *onvif.Device) {
	for retry := 0; ; retry++ {
		if err := dev.Connect(); err == nil {
			break
		} else {
			delay := retryBackoff(retry)
			log.Warn().Err(err).Msgf("[onvif] device %q connect failed, retry in %s", name, delay)
			time.Sleep(delay)
		}
	}

	log.Info().Msgf("[onvif] device %q connected (%s)", name, dev.GetName())

	registerProfileStreams(name, dev)
	startEvents(name, dev)
}

func registerProfileStreams(name string, dev *onvif.Device) {
	profiles, err := dev.GetProfiles()
	if err != nil {
		log.Error().Err(err).Msgf("[onvif] device %q get profiles", name)
		return
	}
	if len(profiles) == 0 {
		log.Warn().Msgf("[onvif] device %q has no profiles", name)
		return
	}

	var mapping []profileStream
	var prefetch []string

	for i, p := range profiles {
		if !dev.ProfileAllowed(p.Token) {
			continue
		}

		// first profile keeps the bare device name; the rest get a suffix
		streamName := name
		if i > 0 {
			streamName = name + "_" + strconv.Itoa(i)
		}

		// don't transcode a JPEG/snapshot profile into h264 video
		srcCodec := onvif.OnvifCodec(p.Codec)
		transcodeThis := dev.Transcoding() && srcCodec != "JPEG"

		// effective codecs advertised to consumers (after any transcode);
		// source cameras speak G.711 audio unless transcoded
		codec := srcCodec
		audio := "G711"
		if transcodeThis {
			if dev.TranscodeVideo() != "" {
				codec = onvif.OnvifCodec(dev.TranscodeVideo())
			}
			if dev.TranscodeAudio() != "" {
				audio = onvif.OnvifAudioCodec(dev.TranscodeAudio())
			}
		}
		ps := profileStream{token: p.Token, stream: streamName, width: p.Width, height: p.Height, codec: codec, audio: audio, vsToken: p.VSToken}

		// credential-free source; ResolveURI injects auth into the resolved URL
		rawSource := "onvif://" + dev.Host() + "?profile=" + url.QueryEscape(p.Token)

		switch {
		case transcodeThis:
			// raw camera stream (the single upstream connection) + an ffmpeg
			// transcode that reads it from go2rtc's own rtsp server. Prefetch
			// targets the EXPOSED (transcoded) stream so ffmpeg stays running
			// and consumers attach to a ready stream instantly — keeping the
			// transcode warm also keeps the one camera connection (its input)
			// warm, so it's still exactly one connection per profile.
			rawName := streamName + "_src"
			exposedSource := "ffmpeg:" + rawName + dev.TranscodeQuery()

			// validate ownership of BOTH names before creating anything, so a
			// failed guard can't leave a just-created orphan raw stream behind
			existingRaw := streams.Get(rawName)
			if existingRaw != nil && !streamBelongsToDevice(existingRaw, dev.Host()) {
				log.Warn().Msgf("[onvif] device %q profile %q: raw stream name %q already used by an unrelated source, profile not exposed", name, p.Token, rawName)
				continue
			}
			existingExposed := streams.Get(streamName)
			if existingExposed != nil && !streamHasSourcePrefix(existingExposed, "ffmpeg:"+rawName) {
				log.Warn().Msgf("[onvif] device %q profile %q: stream name %q already used by an unrelated source, profile not exposed", name, p.Token, streamName)
				continue
			}

			if existingRaw == nil {
				if _, err = streams.New(rawName, rawSource); err != nil {
					log.Error().Err(err).Msgf("[onvif] device %q register raw %q", name, rawName)
					continue
				}
			}
			if existingExposed == nil {
				if _, err = streams.New(streamName, exposedSource); err != nil {
					log.Error().Err(err).Msgf("[onvif] device %q register transcode %q", name, streamName)
					continue
				}
			}

			log.Info().Msgf("[onvif] device %q profile %q -> stream %q (transcode %s)", name, p.Token, streamName, dev.TranscodeQuery())

		default:
			if existing := streams.Get(streamName); existing != nil {
				// don't alias onto an unrelated stream that merely shares the name
				if !streamBelongsToDevice(existing, dev.Host()) {
					log.Warn().Msgf("[onvif] device %q profile %q: stream name %q already used by an unrelated source, profile not exposed", name, p.Token, streamName)
					continue
				}
				log.Debug().Msgf("[onvif] device %q profile %q reuses existing stream %q", name, p.Token, streamName)
			} else if _, err = streams.New(streamName, rawSource); err != nil {
				log.Error().Err(err).Msgf("[onvif] device %q register stream %q", name, streamName)
				continue
			} else {
				log.Info().Msgf("[onvif] device %q profile %q -> stream %q", name, p.Token, streamName)
			}
		}

		mapping = append(mapping, ps)

		if dev.PrefetchProfile(p.Token) {
			prefetch = append(prefetch, streamName)
		}
	}

	// publish the profile mapping BEFORE warming up prefetch: AddPreload blocks
	// several seconds per stream (spawns ffmpeg / dials the camera), and ONVIF
	// clients polling GetProfiles in that window would see an empty device and
	// give up (XMEye NVRs show the channel as "not logged in").
	devicesMu.Lock()
	deviceStreams[name] = mapping
	devicesMu.Unlock()

	go func() {
		for _, streamName := range prefetch {
			if err := streams.AddPreload(streamName, ""); err != nil {
				log.Warn().Err(err).Msgf("[onvif] device %q prefetch %q", name, streamName)
			} else {
				log.Info().Msgf("[onvif] device %q prefetch (always-on) stream %q", name, streamName)
			}
		}
	}()
}

// streamToVSToken maps an emulated VideoSource token back to the camera's real
// one, for proxying imaging requests. The emulated device advertises the single
// shared source "V_SRC_000" (real-camera shape); stream names are accepted too.
// Empty if unknown.
func streamToVSToken(device, token string) string {
	devicesMu.Lock()
	defer devicesMu.Unlock()
	list := deviceStreams[device]
	if token == "V_SRC_000" && len(list) > 0 {
		return list[0].vsToken // the one shared source = the camera's own
	}
	for _, ps := range list {
		if ps.stream == token {
			return ps.vsToken
		}
	}
	return ""
}

// streamToToken maps a go2rtc stream name back to the camera's real profile
// token (the reverse of resolveProfileStream). Empty if not found.
func streamToToken(device, stream string) string {
	devicesMu.Lock()
	defer devicesMu.Unlock()
	for _, ps := range deviceStreams[device] {
		if ps.stream == stream {
			return ps.token
		}
	}
	return ""
}

// tokenToStream maps an emulated profile token (the camera's own token, e.g.
// "000") to the go2rtc stream name that serves it. GetStreamUri/GetSnapshotUri
// use this to build the RTSP/snapshot URL. A stream name is accepted too (so
// pre-token clients still resolve); falls back to the input if unknown.
func tokenToStream(device, token string) string {
	devicesMu.Lock()
	defer devicesMu.Unlock()
	for _, ps := range deviceStreams[device] {
		if ps.token == token {
			return ps.stream
		}
	}
	for _, ps := range deviceStreams[device] {
		if ps.stream == token {
			return ps.stream
		}
	}
	return token
}

// streamBelongsToDevice reports whether an existing stream is backed by an
// onvif:// source pointing at the given device host (i.e. it is this device's
// own stream, not an unrelated stream that happens to share the name).
func streamBelongsToDevice(s *streams.Stream, host string) bool {
	for _, src := range s.Sources() {
		if u, err := url.Parse(src); err == nil && u.Scheme == "onvif" && u.Host == host {
			return true
		}
	}
	return false
}

// streamHasSourcePrefix reports whether any of the stream's sources starts with
// prefix — used to confirm an existing exposed stream is our own ffmpeg
// transcode (source "ffmpeg:<name>_src...") and not an unrelated stream.
func streamHasSourcePrefix(s *streams.Stream, prefix string) bool {
	for _, src := range s.Sources() {
		if strings.HasPrefix(src, prefix) {
			return true
		}
	}
	return false
}

// startDeviceListener serves a dedicated virtual ONVIF device for one camera at
// the root /onvif/* path on its own port. The bind is synchronous (so callers
// learn whether the port is free); serving runs in the background. It starts
// before the upstream camera connects, so the endpoint is reachable — GetProfiles
// simply returns empty until the camera connects and its streams register.
func startDeviceListener(name string, dev *onvif.Device, listen string) error {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/onvif/", deviceONVIFHandler(name, dev))
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	go func() {
		log.Info().Msgf("[onvif] device %q ONVIF listener on %s", name, listen)
		if err := srv.Serve(ln); err != nil {
			log.Error().Err(err).Msgf("[onvif] device %q listener %s", name, listen)
		}
	}()
	return nil
}

func retryBackoff(retry int) time.Duration {
	switch {
	case retry == 0:
		return time.Second
	case retry < 5:
		return 5 * time.Second
	case retry < 10:
		return 10 * time.Second
	default:
		return 60 * time.Second
	}
}
