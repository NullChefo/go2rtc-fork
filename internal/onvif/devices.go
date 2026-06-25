package onvif

import (
	"maps"
	"net"
	"net/http"
	"net/url"
	"strconv"
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
	token  string
	stream string
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

	for i, p := range profiles {
		if !dev.ProfileAllowed(p.Token) {
			continue
		}

		// first profile keeps the bare device name; the rest get a suffix
		streamName := name
		if i > 0 {
			streamName = name + "_" + strconv.Itoa(i)
		}

		if existing := streams.Get(streamName); existing != nil {
			// A stream with this name already exists. If it points at THIS
			// device (a deliberate override or a re-run), keep the mapping so
			// snapshots/emulation resolve to it. If it's an unrelated stream
			// that merely shares the name, do NOT alias to it — that would
			// silently serve the wrong camera. Warn and leave this profile
			// unmapped instead.
			if streamBelongsToDevice(existing, dev.Host()) {
				mapping = append(mapping, profileStream{token: p.Token, stream: streamName})
				log.Debug().Msgf("[onvif] device %q profile %q reuses existing stream %q", name, p.Token, streamName)
			} else {
				log.Warn().Msgf("[onvif] device %q profile %q: stream name %q already used by an unrelated source, profile not exposed", name, p.Token, streamName)
			}
			continue
		}

		// credential-free source; ResolveURI injects auth into the resolved URL
		source := "onvif://" + dev.Host() + "?profile=" + url.QueryEscape(p.Token)

		if _, err = streams.New(streamName, source); err != nil {
			log.Error().Err(err).Msgf("[onvif] device %q register stream %q", name, streamName)
			continue
		}

		mapping = append(mapping, profileStream{token: p.Token, stream: streamName})
		log.Info().Msgf("[onvif] device %q profile %q -> stream %q", name, p.Token, streamName)
	}

	devicesMu.Lock()
	deviceStreams[name] = mapping
	devicesMu.Unlock()
}

// deviceStreamNames returns the go2rtc stream names exposed for a device, in
// profile order (used by the emulated server to list profiles).
func deviceStreamNames(device string) []string {
	devicesMu.Lock()
	defer devicesMu.Unlock()
	list := deviceStreams[device]
	names := make([]string, 0, len(list))
	for _, ps := range list {
		names = append(names, ps.stream)
	}
	return names
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
