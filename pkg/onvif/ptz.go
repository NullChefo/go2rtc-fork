package onvif

import (
	"errors"
	"regexp"
	"strconv"
)

// Preset is a parsed ONVIF PTZ preset.
type Preset struct {
	Token string `json:"token"`
	Name  string `json:"name,omitempty"`
}

// HasPTZ reports whether the camera advertised a PTZ service.
func (d *Device) HasPTZ() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ptzURL != ""
}

func (d *Device) ptzRequest(body string) ([]byte, error) {
	if err := d.Connect(); err != nil {
		return nil, err
	}
	if d.ptzURL == "" {
		return nil, errors.New("onvif: no ptz service")
	}
	return d.request(d.ptzURL, body)
}

func ftoa(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func (d *Device) PTZContinuousMove(token string, pan, tilt, zoom float64) error {
	token = escapeXML(token)
	_, err := d.ptzRequest(`<tptz:ContinuousMove><tptz:ProfileToken>` + token +
		`</tptz:ProfileToken><tptz:Velocity><tt:PanTilt x="` + ftoa(pan) + `" y="` + ftoa(tilt) +
		`"/><tt:Zoom x="` + ftoa(zoom) + `"/></tptz:Velocity></tptz:ContinuousMove>`)
	return err
}

func (d *Device) PTZStop(token string) error {
	token = escapeXML(token)
	_, err := d.ptzRequest(`<tptz:Stop><tptz:ProfileToken>` + token +
		`</tptz:ProfileToken><tptz:PanTilt>true</tptz:PanTilt><tptz:Zoom>true</tptz:Zoom></tptz:Stop>`)
	return err
}

func (d *Device) PTZAbsoluteMove(token string, pan, tilt, zoom float64) error {
	token = escapeXML(token)
	_, err := d.ptzRequest(`<tptz:AbsoluteMove><tptz:ProfileToken>` + token +
		`</tptz:ProfileToken><tptz:Position><tt:PanTilt x="` + ftoa(pan) + `" y="` + ftoa(tilt) +
		`"/><tt:Zoom x="` + ftoa(zoom) + `"/></tptz:Position></tptz:AbsoluteMove>`)
	return err
}

func (d *Device) PTZRelativeMove(token string, pan, tilt, zoom float64) error {
	token = escapeXML(token)
	_, err := d.ptzRequest(`<tptz:RelativeMove><tptz:ProfileToken>` + token +
		`</tptz:ProfileToken><tptz:Translation><tt:PanTilt x="` + ftoa(pan) + `" y="` + ftoa(tilt) +
		`"/><tt:Zoom x="` + ftoa(zoom) + `"/></tptz:Translation></tptz:RelativeMove>`)
	return err
}

func (d *Device) PTZGotoPreset(token, preset string) error {
	_, err := d.ptzRequest(`<tptz:GotoPreset><tptz:ProfileToken>` + escapeXML(token) +
		`</tptz:ProfileToken><tptz:PresetToken>` + escapeXML(preset) + `</tptz:PresetToken></tptz:GotoPreset>`)
	return err
}

var rePreset = regexp.MustCompile(`(?s)<(?:\w+:)?Preset\b[^>]*\btoken="([^"]+)"(.*?)</(?:\w+:)?Preset>`)

func (d *Device) PTZGetPresets(token string) ([]Preset, error) {
	b, err := d.ptzRequest(`<tptz:GetPresets><tptz:ProfileToken>` + escapeXML(token) + `</tptz:ProfileToken></tptz:GetPresets>`)
	if err != nil {
		return nil, err
	}
	var presets []Preset
	for _, m := range rePreset.FindAllSubmatch(b, -1) {
		presets = append(presets, Preset{Token: string(m[1]), Name: FindTagValue(m[2], "Name")})
	}
	return presets, nil
}
