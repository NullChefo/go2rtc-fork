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
	Token string
	Name  string
}

// reProfile matches each <...Profiles token="..."> element, capturing the token.
// Anchored within the opening tag (`[^>]*`) so it never crosses into siblings,
// and `Profiles\b` avoids matching GetProfilesResponse.
var reProfile = regexp.MustCompile(`Profiles\b[^>]*\btoken="([^"]+)"`)

func parseProfiles(b []byte) []Profile {
	var profiles []Profile
	for _, m := range reProfile.FindAllSubmatch(b, -1) {
		profiles = append(profiles, Profile{Token: string(m[1])})
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
