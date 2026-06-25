package onvif

import (
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/AlexxIT/go2rtc/internal/api"
)

// apiSnapshot returns a still image for a configured device.
//
//	GET /api/onvif/snapshot?src=<device>[&profile=<token>][&cache=<dur>]
//
// In the default "stream" mode it redirects to /api/frame.jpeg so the image is
// taken from the already-open shared stream (no extra camera bandwidth). In
// "native" mode it proxies the camera's own JPEG snapshot endpoint.
func apiSnapshot(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	src := query.Get("src")
	if src == "" {
		http.Error(w, "src required", http.StatusBadRequest)
		return
	}

	dev := getDeviceByName(src)
	if dev == nil {
		http.Error(w, "onvif device not found", http.StatusNotFound)
		return
	}

	stream, token, ok := resolveProfileStream(src, query.Get("profile"))
	if !ok {
		http.Error(w, "onvif device not ready", http.StatusServiceUnavailable)
		return
	}

	if dev.SnapshotMode() == "native" {
		img, contentType, err := dev.Snapshot(token)
		if err != nil {
			api.Error(w, err)
			return
		}
		h := w.Header()
		h.Set("Content-Type", contentType)
		h.Set("Cache-Control", "no-cache")
		_, _ = w.Write(img)
		return
	}

	// stream mode: serve the keyframe from the shared stream, reusing the mjpeg
	// handler and its cache. frame.jpeg lives one level up from onvif/snapshot.
	params := url.Values{"src": {stream}}
	if cache := query.Get("cache"); cache != "" {
		params.Set("cache", cache)
	} else {
		params.Set("cache", "1s")
	}

	base := strings.TrimSuffix(r.URL.Path, "onvif/snapshot")
	http.Redirect(w, r, base+"frame.jpeg?"+params.Encode(), http.StatusFound)
}

func qfloat(q url.Values, key string) float64 {
	f, _ := strconv.ParseFloat(q.Get(key), 64)
	return f
}

// ptzToken resolves the camera profile token for a device: explicit &token=,
// else the profile mapped from &profile=, else the device's main profile.
func ptzToken(src string, q url.Values) string {
	if t := q.Get("token"); t != "" {
		return t
	}
	if _, t, ok := resolveProfileStream(src, q.Get("profile")); ok {
		return t
	}
	return ""
}

// apiPTZ controls pan/tilt/zoom on a device.
//
//	/api/onvif/ptz?src=<device>&action=continuous|stop|absolute|relative|preset
//	  &pan=&tilt=&zoom=  (velocity/position) | &preset=<token>  | &token=<profile>
func apiPTZ(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	src := q.Get("src")

	dev := getDeviceByName(src)
	if dev == nil {
		http.Error(w, "onvif device not found", http.StatusNotFound)
		return
	}
	if !dev.HasPTZ() {
		http.Error(w, "device has no ptz service", http.StatusNotImplemented)
		return
	}

	token := ptzToken(src, q)
	if token == "" {
		http.Error(w, "onvif device not ready (no profile token)", http.StatusServiceUnavailable)
		return
	}

	var err error
	switch q.Get("action") {
	case "", "continuous":
		err = dev.PTZContinuousMove(token, qfloat(q, "pan"), qfloat(q, "tilt"), qfloat(q, "zoom"))
	case "stop":
		err = dev.PTZStop(token)
	case "absolute":
		err = dev.PTZAbsoluteMove(token, qfloat(q, "pan"), qfloat(q, "tilt"), qfloat(q, "zoom"))
	case "relative":
		err = dev.PTZRelativeMove(token, qfloat(q, "pan"), qfloat(q, "tilt"), qfloat(q, "zoom"))
	case "preset":
		err = dev.PTZGotoPreset(token, q.Get("preset"))
	default:
		http.Error(w, "unknown ptz action", http.StatusBadRequest)
		return
	}
	if err != nil {
		api.Error(w, err)
		return
	}
	api.ResponseJSON(w, map[string]string{"status": "ok"})
}

// apiPresets lists PTZ presets: /api/onvif/presets?src=<device>[&profile=|&token=]
func apiPresets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	src := q.Get("src")

	dev := getDeviceByName(src)
	if dev == nil {
		http.Error(w, "onvif device not found", http.StatusNotFound)
		return
	}

	token := ptzToken(src, q)
	if token == "" {
		http.Error(w, "onvif device not ready (no profile token)", http.StatusServiceUnavailable)
		return
	}

	presets, err := dev.PTZGetPresets(token)
	if err != nil {
		api.Error(w, err)
		return
	}
	api.ResponseJSON(w, map[string]any{"presets": presets})
}

// apiImaging returns raw imaging settings:
// /api/onvif/imaging?src=<device>&token=<videoSourceToken>
func apiImaging(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	dev := getDeviceByName(q.Get("src"))
	if dev == nil {
		http.Error(w, "onvif device not found", http.StatusNotFound)
		return
	}

	token := q.Get("token")
	if token == "" {
		http.Error(w, "token (video source token) required", http.StatusBadRequest)
		return
	}

	b, err := dev.GetImagingSettings(token)
	if err != nil {
		api.Error(w, err)
		return
	}
	api.Response(w, b, "application/soap+xml; charset=utf-8")
}

var reSOAPBody = regexp.MustCompile(`(?s)<(?:\w+:)?Body\b[^>]*>(.*)</(?:\w+:)?Body>`)

// extractSOAPBody returns the inner XML of a SOAP <Body>, or the whole input if
// the caller posted a bare operation fragment.
func extractSOAPBody(b []byte) string {
	if m := reSOAPBody.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return string(b)
}

// apiSOAP is a generic authenticated ONVIF reverse-proxy: it forwards an
// arbitrary SOAP operation to the camera re-signed with the device session.
//
//	POST /api/onvif/soap?src=<device>&service=device|media|imaging|events|ptz
func apiSOAP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusBadRequest)
		return
	}

	q := r.URL.Query()
	dev := getDeviceByName(q.Get("src"))
	if dev == nil {
		http.Error(w, "onvif device not found", http.StatusNotFound)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	out, err := dev.ProxySOAP(q.Get("service"), extractSOAPBody(body))
	if err != nil {
		api.Error(w, err)
		return
	}
	api.Response(w, out, "application/soap+xml; charset=utf-8")
}

// apiInfo returns device metadata + the profile→stream mapping.
func apiInfo(w http.ResponseWriter, r *http.Request) {
	src := r.URL.Query().Get("src")

	dev := getDeviceByName(src)
	if dev == nil {
		http.Error(w, "onvif device not found", http.StatusNotFound)
		return
	}

	info := dev.Information()
	profiles, _ := dev.GetProfiles()

	type prof struct {
		Token  string `json:"token"`
		Stream string `json:"stream,omitempty"`
	}
	ps := make([]prof, 0, len(profiles))
	for _, p := range profiles {
		stream, _, _ := resolveProfileStream(src, p.Token)
		ps = append(ps, prof{Token: p.Token, Stream: stream})
	}

	api.ResponseJSON(w, map[string]any{
		"name":         dev.GetName(),
		"manufacturer": info.Manufacturer,
		"model":        info.Model,
		"firmware":     info.Firmware,
		"serial":       info.Serial,
		"hasEvents":    dev.HasEvents(),
		"hasPTZ":       dev.HasPTZ(),
		"snapshot":     dev.SnapshotMode(),
		"profiles":     ps,
	})
}
