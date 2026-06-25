package onvif

import (
	"errors"
	"html"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// Profile is a parsed ONVIF media profile.
type Profile struct {
	Token  string
	Name   string
	Width  int
	Height int
	Codec  string // VideoEncoder encoding: H264 / H265 / JPEG
}

// reProfileBlock captures each full <...Profiles token="...">...</...Profiles>
// block so per-profile fields (encoding, resolution) can be extracted from it.
var reProfileBlock = regexp.MustCompile(`(?s)<(?:\w+:)?Profiles\b[^>]*\btoken="([^"]+)".*?</(?:\w+:)?Profiles>`)

// reProfile is the token-only fallback for cameras whose GetProfiles XML the
// block regex can't bracket (self-closed / malformed).
var reProfile = regexp.MustCompile(`Profiles\b[^>]*\btoken="([^"]+)"`)

// reVEC isolates the VideoEncoderConfiguration so codec/resolution are read from
// it and not from an AudioEncoderConfiguration's Encoding or the VideoSource
// Bounds (cameras frequently violate element ordering).
var reVEC = regexp.MustCompile(`(?s)<(?:\w+:)?VideoEncoderConfiguration\b.*?</(?:\w+:)?VideoEncoderConfiguration>`)

func parseProfiles(b []byte) []Profile {
	var profiles []Profile
	for _, m := range reProfileBlock.FindAllSubmatch(b, -1) {
		block := m[0]
		vec := block
		if v := reVEC.Find(block); v != nil {
			vec = v
		}
		profiles = append(profiles, Profile{
			Token:  string(m[1]),
			Name:   FindTagValue(block, "Name"),   // profile name (first <Name>)
			Codec:  FindTagValue(vec, "Encoding"), // video encoding, scoped to the VEC
			Width:  atoi(FindTagValue(vec, "Width")),
			Height: atoi(FindTagValue(vec, "Height")),
		})
	}
	if len(profiles) == 0 {
		for _, m := range reProfile.FindAllSubmatch(b, -1) {
			profiles = append(profiles, Profile{Token: string(m[1])})
		}
	}
	return profiles
}

// GetProfiles returns the camera's media profiles, fetching and caching them on
// first call.
func (d *Device) GetProfiles() ([]Profile, error) {
	if err := d.Connect(); err != nil {
		return nil, err
	}

	d.mu.Lock()
	if d.profiles != nil {
		profiles := d.profiles
		d.mu.Unlock()
		return profiles, nil
	}
	d.mu.Unlock()

	b, err := d.request(d.mediaURL, `<trt:GetProfiles/>`)
	if err != nil {
		return nil, err
	}

	profiles := parseProfiles(b)

	d.mu.Lock()
	d.profiles = profiles
	d.mu.Unlock()

	return profiles, nil
}

// ProfileAllowed reports whether the profile token passes the optional config
// allowlist (empty allowlist = all profiles).
func (d *Device) ProfileAllowed(token string) bool {
	return len(d.config.Profiles) == 0 || slices.Contains(d.config.Profiles, token)
}

// PrefetchProfile reports whether this profile should be kept always connected
// (prefetch: ['<token>', ...] or prefetch: ['*'] for all).
func (d *Device) PrefetchProfile(token string) bool {
	return slices.Contains(d.config.Prefetch, "*") || slices.Contains(d.config.Prefetch, token)
}

// Transcoding reports whether the device re-encodes its exposed streams.
func (d *Device) Transcoding() bool {
	t := d.config.Transcode
	return t != nil && (t.Video != "" || t.Audio != "")
}

// TranscodeVideo returns the configured target video codec ("" if none).
func (d *Device) TranscodeVideo() string {
	if d.config.Transcode == nil {
		return ""
	}
	return d.config.Transcode.Video
}

// TranscodeQuery builds the go2rtc ffmpeg source suffix, e.g.
// "#video=h264#audio=aac#hardware=vaapi". The untranscoded track is "copy" so it
// is never dropped.
func (d *Device) TranscodeQuery() string {
	t := d.config.Transcode
	video, audio := "copy", "copy"
	if t.Video != "" {
		video = t.Video
	}
	if t.Audio != "" {
		audio = t.Audio
	}
	q := "#video=" + video + "#audio=" + audio
	if t.Hardware != "" {
		q += "#hardware=" + t.Hardware
	}
	return q
}

// OnvifCodec normalises a codec name (config or parsed) to the ONVIF Encoding
// form (H264 / H265 / JPEG).
func OnvifCodec(codec string) string {
	switch strings.ToLower(codec) {
	case "h264", "avc":
		return "H264"
	case "h265", "hevc":
		return "H265"
	case "mjpeg", "jpeg":
		return "JPEG"
	default:
		// unknown / empty -> safe default; never advertise a non-video token
		// (e.g. a stray audio "AAC") as a video Encoding
		return "H264"
	}
}

func (d *Device) GetStreamUri(token string) ([]byte, error) {
	return d.request(d.mediaURL, `<trt:GetStreamUri>
	<trt:StreamSetup>
		<tt:Stream>RTP-Unicast</tt:Stream>
		<tt:Transport><tt:Protocol>RTSP</tt:Protocol></tt:Transport>
	</trt:StreamSetup>
	<trt:ProfileToken>`+escapeXML(token)+`</trt:ProfileToken>
</trt:GetStreamUri>`)
}

func (d *Device) GetSnapshotUri(token string) ([]byte, error) {
	return d.request(d.mediaURL, `<trt:GetSnapshotUri><trt:ProfileToken>`+escapeXML(token)+`</trt:ProfileToken></trt:GetSnapshotUri>`)
}

// ResolveURI resolves an onvif:// query (from a stream source) to a concrete
// RTSP or snapshot URL using the cached session. It mirrors Client.GetURI but
// adds an unambiguous "profile" token param (used by auto-registered streams);
// "subtype" keeps the legacy index-or-token behaviour for hand-written sources.
func (d *Device) ResolveURI(query url.Values) (string, error) {
	if err := d.Connect(); err != nil {
		return "", err
	}

	token := query.Get("profile")
	if token == "" {
		token = query.Get("subtype")
		// numeric subtype => index into the profile list
		if i := atoi(token); i >= 0 {
			profiles, err := d.GetProfiles()
			if err != nil {
				return "", err
			}
			if i >= len(profiles) {
				return "", errors.New("onvif: wrong subtype")
			}
			token = profiles[i].Token
		}
	}

	get := d.GetStreamUri
	if query.Has("snapshot") {
		get = d.GetSnapshotUri
	}

	b, err := get(token)
	if err != nil {
		return "", err
	}

	rawURL := strings.TrimSpace(html.UnescapeString(FindTagValue(b, "Uri")))
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}

	if u.User == nil && d.url.User != nil {
		u.User = d.url.User
	}

	return u.String(), nil
}
