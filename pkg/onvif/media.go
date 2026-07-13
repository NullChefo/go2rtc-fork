package onvif

import (
	"bytes"
	"errors"
	"html"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// Profile is a parsed ONVIF media profile.
type Profile struct {
	Token   string
	Name    string
	Width   int
	Height  int
	Codec   string // VideoEncoder encoding: H264 / H265 / JPEG
	VSToken string // VideoSource token (needed to proxy imaging requests)
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

// reVSC isolates the VideoSourceConfiguration to extract the real VideoSource
// token (SourceToken), which imaging requests must address.
var reVSC = regexp.MustCompile(`(?s)<(?:\w+:)?VideoSourceConfiguration\b.*?</(?:\w+:)?VideoSourceConfiguration>`)

func parseProfiles(b []byte) []Profile {
	var profiles []Profile
	for _, m := range reProfileBlock.FindAllSubmatch(b, -1) {
		block := m[0]
		vec := block
		if v := reVEC.Find(block); v != nil {
			vec = v
		}
		var vsToken string
		if v := reVSC.Find(block); v != nil {
			vsToken = FindTagValue(v, "SourceToken")
		}
		profiles = append(profiles, Profile{
			Token:   string(m[1]),
			Name:    FindTagValue(block, "Name"),   // profile name (first <Name>)
			Codec:   FindTagValue(vec, "Encoding"), // video encoding, scoped to the VEC
			Width:   atoi(FindTagValue(vec, "Width")),
			Height:  atoi(FindTagValue(vec, "Height")),
			VSToken: vsToken,
		})
	}
	if len(profiles) == 0 {
		for _, m := range reProfile.FindAllSubmatch(b, -1) {
			profiles = append(profiles, Profile{Token: string(m[1])})
		}
	}
	return profiles
}

// getProfilesResponse returns the camera's original GetProfiles SOAP response,
// fetching and caching both the raw XML and its parsed profile summary on the
// first call. Keeping the original bytes allows a virtual ONVIF device to
// preserve vendor extensions and schema quirks used by strict NVR filters.
func (d *Device) getProfilesResponse() ([]byte, error) {
	if err := d.Connect(); err != nil {
		return nil, err
	}

	d.mu.Lock()
	if d.profilesRaw != nil {
		b := bytes.Clone(d.profilesRaw)
		d.mu.Unlock()
		return b, nil
	}
	d.mu.Unlock()

	b, err := d.request(d.mediaURL, `<trt:GetProfiles/>`)
	if err != nil {
		return nil, err
	}

	profiles := parseProfiles(b)

	d.mu.Lock()
	// Another concurrent caller may have completed the same first fetch. Keep
	// the first complete response as the stable device description.
	if d.profilesRaw == nil {
		d.profilesRaw = bytes.Clone(b)
		d.profiles = profiles
	}
	b = bytes.Clone(d.profilesRaw)
	d.mu.Unlock()

	return b, nil
}

// GetProfiles returns the camera's parsed media profile summary. The returned
// slice is a copy so callers cannot mutate the persistent device cache.
func (d *Device) GetProfiles() ([]Profile, error) {
	if _, err := d.getProfilesResponse(); err != nil {
		return nil, err
	}

	d.mu.Lock()
	profiles := slices.Clone(d.profiles)
	d.mu.Unlock()
	return profiles, nil
}

// GetProfilesRaw returns an exact copy of the upstream camera's original
// GetProfiles SOAP envelope. GetStreamUri is intentionally not part of this
// response and remains intercepted by the virtual device.
func (d *Device) GetProfilesRaw() ([]byte, error) {
	return d.getProfilesResponse()
}

// MirrorProfiles reports whether the virtual ONVIF device should return the
// upstream camera's original GetProfiles response instead of synthesizing one.
func (d *Device) MirrorProfiles() bool {
	return d.config.MirrorProfiles
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
	if t == nil {
		return ""
	}
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

// OnvifAudioCodec normalises an audio codec name to the ONVIF AudioEncoding
// enum (G711 / G726 / AAC). Camera sources here are G.711; transcode may
// produce AAC.
func OnvifAudioCodec(codec string) string {
	switch strings.ToLower(codec) {
	case "aac":
		return "AAC"
	case "g726":
		return "G726"
	default: // pcma, pcmu, g711, unknown
		return "G711"
	}
}

// TranscodeAudio returns the configured target audio codec ("" if none).
func (d *Device) TranscodeAudio() string {
	if d.config.Transcode == nil {
		return ""
	}
	return d.config.Transcode.Audio
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
