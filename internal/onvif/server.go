package onvif

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/AlexxIT/go2rtc/internal/api"
	"github.com/AlexxIT/go2rtc/internal/rtsp"
	"github.com/AlexxIT/go2rtc/pkg/onvif"
)

// onvifRouter dispatches /onvif/... requests. A path of /onvif/<device>/<svc>
// for a configured device is served by the per-device emulation; everything
// else (legacy /onvif/device_service, etc.) falls through to the aggregate
// device, preserving existing behaviour.
func onvifRouter(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/onvif/"), "/")
	if i := strings.IndexByte(rest, '/'); i > 0 {
		if name := rest[:i]; getDeviceByName(name) != nil {
			onvifEmulateDevice(w, r, name, getDeviceByName(name), rest[i+1:], "/onvif/"+name)
			return
		}
	}
	onvifDeviceService(w, r)
}

// deviceONVIFHandler serves the ONVIF emulation for ONE device on its own
// dedicated listener, where the device sits at the root path
// (/onvif/device_service, /onvif/media_service, /onvif/Subscription, ...).
func deviceONVIFHandler(name string, dev *onvif.Device) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		service := strings.TrimPrefix(r.URL.Path, "/onvif/")
		onvifEmulateDevice(w, r, name, dev, service, "/onvif")
	}
}

func hostOnly(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}

// onvifEmulateDevice makes go2rtc look like the real camera to a downstream
// ONVIF client: video/snapshots resolve to the shared go2rtc streams, events
// are brokered from the upstream subscription, and PTZ/imaging are proxied.
func onvifEmulateDevice(w http.ResponseWriter, r *http.Request, name string, dev *onvif.Device, service, prefix string) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	operation := onvif.GetRequestAction(b)
	if operation == "" {
		http.Error(w, "malformed request body", http.StatusBadRequest)
		return
	}

	log.Trace().Msgf("[onvif] emulate %s/%s op=%s:\n%s", name, service, operation, b)

	var resp []byte
	switch {
	case strings.HasPrefix(service, "ptz"):
		// proxy every PTZ op to the real camera, remapping our stream-name
		// profile tokens back to the camera's real tokens
		resp, err = dev.ProxySOAP("ptz", remapProfileToken(name, extractSOAPBody(b)))
	case strings.HasPrefix(service, "imaging"):
		resp, err = dev.ProxySOAP("imaging", extractSOAPBody(b))
	case strings.HasPrefix(service, "event") && operation == onvif.ServiceGetServiceCapabilities:
		// per-service capabilities must use the event namespace, not media
		resp = onvif.EventServiceCapabilitiesResponse()
	default:
		resp, err = emulateOperation(operation, b, name, dev, r, prefix)
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		log.Debug().Err(err).Msgf("[onvif] emulate %s op=%s", name, operation)
		return
	}
	if resp == nil {
		http.Error(w, "unsupported operation", http.StatusBadRequest)
		log.Warn().Msgf("[onvif] emulate %s unsupported op=%s", name, operation)
		return
	}

	log.Trace().Msgf("[onvif] emulate response:\n%s", resp)
	w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
	if _, err = w.Write(resp); err != nil {
		log.Error().Err(err).Caller().Send()
	}
}

func emulateOperation(operation string, b []byte, name string, dev *onvif.Device, r *http.Request, prefix string) ([]byte, error) {
	infos := profileInfos(name)

	switch operation {
	// ---- device service ----
	case onvif.DeviceGetCapabilities:
		return onvif.DeviceCapabilities(r.Host, prefix, dev.HasPTZ()), nil
	case onvif.DeviceGetServices:
		return onvif.DeviceServices(r.Host, prefix, dev.HasPTZ()), nil
	case onvif.DeviceGetDeviceInformation:
		info := dev.Information()
		// serial = go2rtc device name => stable unique id per emulated camera
		return onvif.GetDeviceInformationResponse(info.Manufacturer, info.Model, info.Firmware, name), nil

	case onvif.ServiceGetServiceCapabilities,
		onvif.DeviceGetNetworkInterfaces,
		onvif.DeviceGetSystemDateAndTime,
		onvif.DeviceSetSystemDateAndTime,
		onvif.DeviceGetDiscoveryMode,
		onvif.DeviceGetDNS,
		onvif.DeviceGetHostname,
		onvif.DeviceGetNetworkDefaultGateway,
		onvif.DeviceGetNetworkProtocols,
		onvif.DeviceGetNTP,
		onvif.DeviceGetScopes,
		onvif.MediaGetVideoEncoderConfiguration,
		onvif.MediaGetAudioEncoderConfigurations,
		onvif.MediaGetVideoEncoderConfigurationOptions,
		onvif.MediaGetAudioSources,
		onvif.MediaGetAudioSourceConfigurations:
		return onvif.StaticResponse(operation), nil

	// ---- media service (real resolution + effective codec) ----
	case onvif.MediaGetVideoSources:
		return onvif.DeviceVideoSourcesResponse(infos), nil
	case onvif.MediaGetProfiles:
		return onvif.DeviceProfilesResponse(infos, dev.HasPTZ()), nil
	case onvif.MediaGetProfile:
		return onvif.DeviceProfileResponse(profileInfo(name, onvif.FindTagValue(b, "ProfileToken")), dev.HasPTZ()), nil
	case onvif.MediaGetVideoSourceConfigurations:
		return onvif.DeviceVideoSourceConfigurationsResponse(infos), nil
	case onvif.MediaGetVideoSourceConfiguration:
		return onvif.DeviceVideoSourceConfigurationResponse(profileInfo(name, onvif.FindTagValue(b, "ConfigurationToken"))), nil
	case onvif.MediaGetVideoEncoderConfigurations:
		return onvif.DeviceVideoEncoderConfigurationsResponse(infos), nil
	case onvif.MediaGetStreamUri:
		// RTSP is always served by go2rtc's rtsp server, not this ONVIF port.
		// JoinHostPort keeps IPv6 literals bracketed; omit the port if rtsp is
		// disabled (the device can't stream then, but don't emit "host:/path").
		host := hostOnly(r.Host)
		if rtsp.Port != "" {
			host = net.JoinHostPort(host, rtsp.Port)
		}
		uri := "rtsp://" + host + "/" + onvif.FindTagValue(b, "ProfileToken")
		return onvif.GetStreamUriResponse(uri), nil
	case onvif.MediaGetSnapshotUri:
		// snapshots are served by the HTTP API port (a dedicated ONVIF listener
		// only serves /onvif/*), so point the URI at api.Port. Fall back to the
		// port this request arrived on if the api TCP listener is disabled.
		port := api.Port
		if port == 0 {
			if _, p, e := net.SplitHostPort(r.Host); e == nil {
				port, _ = strconv.Atoi(p)
			}
		}
		host := net.JoinHostPort(hostOnly(r.Host), strconv.Itoa(port))
		uri := "http://" + host + "/api/frame.jpeg?src=" + url.QueryEscape(onvif.FindTagValue(b, "ProfileToken")) + "&cache=1s"
		return onvif.GetSnapshotUriResponse(uri), nil

	// ---- event service (broker the upstream subscription) ----
	case onvif.EventsGetEventProperties:
		return onvif.GetEventPropertiesResponse(), nil
	case onvif.EventsCreatePullPointSubscription:
		id := emuSubscribe(name)
		addr := "http://" + r.Host + prefix + "/Subscription?Idx=" + id
		return onvif.CreatePullPointSubscriptionResponse(addr, time.Now()), nil
	case onvif.EventsPullMessages:
		to := 30 * time.Second
		if s := onvif.FindTagValue(b, "Timeout"); s != "" {
			if d := parsePT(s); d > 0 {
				to = d
			}
		}
		if to > 60*time.Second {
			to = 60 * time.Second
		}
		events, ok := emuPull(r.URL.Query().Get("Idx"), to)
		if !ok {
			return nil, errors.New("unknown subscription")
		}
		return onvif.PullMessagesResponse(events, time.Now()), nil
	case onvif.EventsRenew:
		emuRenew(r.URL.Query().Get("Idx"))
		return onvif.RenewResponse(time.Now()), nil
	case onvif.EventsUnsubscribe:
		emuUnsubscribe(r.URL.Query().Get("Idx"))
		return onvif.UnsubscribeResponse(), nil
	}

	return nil, nil
}

var reProfileTokenTag = regexp.MustCompile(`(<(?:\w+:)?ProfileToken>)([^<]+)(</(?:\w+:)?ProfileToken>)`)

// remapProfileToken rewrites the ProfileToken in a downstream SOAP fragment
// (which carries our stream name) to the camera's real profile token before the
// request is proxied to the real PTZ service.
func remapProfileToken(device, frag string) string {
	return reProfileTokenTag.ReplaceAllStringFunc(frag, func(m string) string {
		sub := reProfileTokenTag.FindStringSubmatch(m)
		token := streamToToken(device, sub[2])
		if token == "" {
			token = sub[2]
		}
		return sub[1] + token + sub[3]
	})
}

var (
	rePTHour = regexp.MustCompile(`(\d+)H`)
	rePTMin  = regexp.MustCompile(`(\d+)M`)
	rePTSec  = regexp.MustCompile(`(\d+)S`)
)

// parsePT parses an ISO-8601 duration like PT30S / PT1M / PT1H into a Duration.
func parsePT(s string) time.Duration {
	var d time.Duration
	if m := rePTHour.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		d += time.Duration(n) * time.Hour
	}
	if m := rePTMin.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		d += time.Duration(n) * time.Minute
	}
	if m := rePTSec.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		d += time.Duration(n) * time.Second
	}
	return d
}
