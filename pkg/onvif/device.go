package onvif

import (
	"bytes"
	"errors"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DeviceConfig describes a persistent ONVIF camera from the go2rtc config
// (the `onvif.devices` section).
type DeviceConfig struct {
	URL      string   `yaml:"url"`      // onvif://user:pass@host[:port][/path]
	Name     string   `yaml:"name"`     // optional display name override
	Snapshot string   `yaml:"snapshot"` // "stream" (default, keyframe from shared stream) | "native" (camera JPEG endpoint)
	Events   *bool    `yaml:"events"`   // subscribe to camera events (default true)
	Listen   string   `yaml:"listen"`   // serve a dedicated virtual ONVIF device on this addr (e.g. ":8901")
	Prefetch []string `yaml:"prefetch"` // profile tokens (or "*") to keep always connected (warm)
	Profiles []string `yaml:"profiles"` // optional profile-token allowlist (empty = all)
}

// DeviceInformation holds the static data from GetDeviceInformation.
type DeviceInformation struct {
	Manufacturer string
	Model        string
	Firmware     string
	Serial       string
}

// Device is a stateful, reusable ONVIF session. Unlike Client (created and
// discarded on every dial), a Device discovers the camera's service URLs once,
// caches its profiles, and reuses a single keep-alive HTTP client for all SOAP
// calls. Many go2rtc streams that point at the same camera share one Device,
// so the camera sees a single control session regardless of consumer count.
type Device struct {
	config DeviceConfig
	url    *url.URL

	deviceURL  string
	mediaURL   string
	imagingURL string
	eventsURL  string
	ptzURL     string

	client      *http.Client // control calls (short timeout)
	eventClient *http.Client // event long-poll (no global timeout; context-governed)

	mu        sync.Mutex
	connected bool
	hasEvents bool
	info      DeviceInformation
	profiles  []Profile
}

func NewDevice(config DeviceConfig) (*Device, error) {
	u, err := url.Parse(config.URL)
	if err != nil {
		return nil, err
	}
	if u.Host == "" {
		return nil, errors.New("onvif: device url without host")
	}

	d := &Device{
		config: config,
		url:    u,
		client: &http.Client{
			Timeout:   15 * time.Second,
			Transport: &http.Transport{MaxIdleConnsPerHost: 2},
		},
		// no global timeout: PullMessages is a long-poll governed by a context
		eventClient: &http.Client{
			Transport: &http.Transport{MaxIdleConnsPerHost: 2},
		},
	}
	return d, nil
}

// Host returns the camera host[:port], used to map onvif:// stream sources back
// to this device.
func (d *Device) Host() string {
	return d.url.Host
}

// Connect performs the one-time capability discovery (service URLs + device
// info). It is idempotent and safe for concurrent callers; the per-request
// WS-Security digest is regenerated on every call, so a long-lived session does
// not go stale across camera reboots.
func (d *Device) Connect() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.connected {
		return nil
	}

	baseURL := "http://" + d.url.Host
	d.deviceURL = baseURL + GetPath(d.url.Path, PathDevice)

	b, err := d.request(d.deviceURL, `<tds:GetCapabilities><tds:Category>All</tds:Category></tds:GetCapabilities>`)
	if err != nil {
		return err
	}

	// rebuild every service URL on the configured host so cameras that report
	// 0.0.0.0 or an internal address in their XAddr still work (same trick as
	// NewClient)
	d.mediaURL = baseURL + GetPath(FindTagValue(b, "Media.+?XAddr"), "/onvif/media_service")
	d.imagingURL = baseURL + GetPath(FindTagValue(b, "Imaging.+?XAddr"), "/onvif/imaging_service")
	if s := FindTagValue(b, "Events.+?XAddr"); s != "" {
		d.eventsURL = baseURL + GetPath(s, "/onvif/event_service")
		d.hasEvents = true // camera advertises an event service
	} else {
		d.eventsURL = baseURL + "/onvif/event_service"
	}
	if s := FindTagValue(b, "PTZ.+?XAddr"); s != "" {
		d.ptzURL = baseURL + GetPath(s, "/onvif/ptz_service")
	}

	// best-effort device info (non-fatal)
	if b, err = d.request(d.deviceURL, `<tds:GetDeviceInformation/>`); err == nil {
		d.info = DeviceInformation{
			Manufacturer: FindTagValue(b, "Manufacturer"),
			Model:        FindTagValue(b, "Model"),
			Firmware:     FindTagValue(b, "FirmwareVersion"),
			Serial:       FindTagValue(b, "SerialNumber"),
		}
	}

	d.connected = true
	return nil
}

// HasEvents reports whether the camera advertised an event service.
func (d *Device) HasEvents() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.hasEvents
}

// Information returns the cached GetDeviceInformation data.
func (d *Device) Information() DeviceInformation {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.info
}

// ServiceURL returns the discovered URL for a service ("device", "media",
// "imaging", "events", "ptz"), defaulting to the device service.
func (d *Device) ServiceURL(service string) string {
	switch service {
	case "media":
		return d.mediaURL
	case "imaging":
		return d.imagingURL
	case "events":
		return d.eventsURL
	case "ptz":
		return d.ptzURL
	default:
		return d.deviceURL
	}
}

// ProxySOAP forwards a raw SOAP body fragment to one of the camera's services,
// re-signed with the device session's WS-Security. This is the generic escape
// hatch for ONVIF operations without a typed wrapper.
func (d *Device) ProxySOAP(service, body string) ([]byte, error) {
	if err := d.Connect(); err != nil {
		return nil, err
	}
	return d.request(d.ServiceURL(service), body)
}

// EventsEnabled reports whether event subscription is enabled in config (default true).
func (d *Device) EventsEnabled() bool {
	return d.config.Events == nil || *d.config.Events
}

// GetName returns a human-friendly device label for logs.
func (d *Device) GetName() string {
	if d.config.Name != "" {
		return d.config.Name
	}
	d.mu.Lock()
	info := d.info
	d.mu.Unlock()
	if name := strings.TrimSpace(info.Manufacturer + " " + info.Model); name != "" {
		return name
	}
	return d.url.Host
}

// request posts an authenticated SOAP body to the given service URL, reusing the
// persistent HTTP client. It mirrors Client.Request but on the shared session.
func (d *Device) request(serviceURL, body string) ([]byte, error) {
	if serviceURL == "" {
		return nil, errors.New("onvif: unsupported service")
	}

	e := NewEnvelopeWithUser(d.url.User)
	e.Append(body)

	res, err := d.client.Post(serviceURL, "application/soap+xml;charset=utf-8", bytes.NewReader(e.Bytes()))
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, errors.New("onvif: wrong response " + res.Status)
	}

	return io.ReadAll(res.Body)
}

// SnapshotMode returns "native" or "stream" (default).
func (d *Device) SnapshotMode() string {
	if d.config.Snapshot == "native" {
		return "native"
	}
	return "stream"
}

// Snapshot fetches a still image directly from the camera's ONVIF snapshot
// endpoint (snapshot: native mode). It resolves the snapshot URI on the shared
// session, then GETs the image with Basic auth through the same HTTP client.
func (d *Device) Snapshot(token string) (img []byte, contentType string, err error) {
	if err = d.Connect(); err != nil {
		return nil, "", err
	}

	b, err := d.GetSnapshotUri(token)
	if err != nil {
		return nil, "", err
	}

	rawURL := strings.TrimSpace(html.UnescapeString(FindTagValue(b, "Uri")))
	if rawURL == "" {
		return nil, "", errors.New("onvif: empty snapshot uri")
	}

	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	if d.url.User != nil {
		pass, _ := d.url.User.Password()
		req.SetBasicAuth(d.url.User.Username(), pass)
	}

	res, err := d.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, "", errors.New("onvif: snapshot status " + res.Status)
	}

	if img, err = io.ReadAll(res.Body); err != nil {
		return nil, "", err
	}

	if contentType = res.Header.Get("Content-Type"); contentType == "" {
		contentType = "image/jpeg"
	}
	return img, contentType, nil
}
