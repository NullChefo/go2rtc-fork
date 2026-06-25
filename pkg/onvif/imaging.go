package onvif

import "errors"

// GetImagingSettings returns the raw imaging settings SOAP for a video source
// token. Setting imaging values is available via ProxySOAP (service=imaging).
func (d *Device) GetImagingSettings(videoSourceToken string) ([]byte, error) {
	if err := d.Connect(); err != nil {
		return nil, err
	}
	if d.imagingURL == "" {
		return nil, errors.New("onvif: no imaging service")
	}
	return d.request(d.imagingURL,
		`<timg:GetImagingSettings><timg:VideoSourceToken>`+escapeXML(videoSourceToken)+`</timg:VideoSourceToken></timg:GetImagingSettings>`)
}
