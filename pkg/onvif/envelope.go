package onvif

import (
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
)

var xmlReplacer = strings.NewReplacer(
	`&`, `&amp;`,
	`<`, `&lt;`,
	`>`, `&gt;`,
	`"`, `&quot;`,
	`'`, `&apos;`,
)

// escapeXML escapes a value for safe inclusion in XML text or attribute content.
// All ONVIF SOAP is built by string concatenation (Appendf), so any externally
// sourced value (camera event data, stream/profile tokens, URIs with query
// params) must pass through this before interpolation.
func escapeXML(s string) string { return xmlReplacer.Replace(s) }

// EscapeXML is the exported form, for callers outside this package (e.g. the
// WS-Discovery responder building Probe/Hello XML).
func EscapeXML(s string) string { return escapeXML(s) }

type Envelope struct {
	buf []byte
}

const (
	prefix1 = `<?xml version="1.0" encoding="utf-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:tt="http://www.onvif.org/ver10/schema" xmlns:tds="http://www.onvif.org/ver10/device/wsdl" xmlns:trt="http://www.onvif.org/ver10/media/wsdl" xmlns:tev="http://www.onvif.org/ver10/events/wsdl" xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl" xmlns:timg="http://www.onvif.org/ver20/imaging/wsdl" xmlns:wsnt="http://docs.oasis-open.org/wsn/b-2" xmlns:wsa="http://www.w3.org/2005/08/addressing">`
	prefix2 = `<s:Body>`
	suffix  = `</s:Body></s:Envelope>`
)

func NewEnvelope() *Envelope {
	e := &Envelope{buf: make([]byte, 0, 1024)}
	e.Append(prefix1, prefix2)
	return e
}

func NewEnvelopeWithUser(user *url.Userinfo) *Envelope {
	if user == nil {
		return NewEnvelope()
	}

	e := &Envelope{buf: make([]byte, 0, 1024)}
	e.Append(prefix1, `<s:Header>`, securityHeader(user), `</s:Header>`, prefix2)
	return e
}

// NewEnvelopeWithAction adds WS-Addressing headers (wsa:Action / wsa:To /
// wsa:MessageID) alongside WS-Security. Many cameras require these for the event
// PullPoint and subscription-manager operations.
func NewEnvelopeWithAction(user *url.Userinfo, action, to string) *Envelope {
	e := &Envelope{buf: make([]byte, 0, 1024)}
	e.Append(prefix1, `<s:Header>`)
	if action != "" {
		e.Appendf(`<wsa:Action>%s</wsa:Action>`, action)
	}
	if to != "" {
		e.Appendf(`<wsa:To>%s</wsa:To>`, to)
	}
	e.Appendf(`<wsa:MessageID>urn:uuid:%s</wsa:MessageID>`, UUID())
	if user != nil {
		e.Append(securityHeader(user))
	}
	e.Append(`</s:Header>`, prefix2)
	return e
}

func securityHeader(user *url.Userinfo) string {
	nonce := core.RandString(16, 36)
	created := time.Now().UTC().Format(time.RFC3339Nano)
	pass, _ := user.Password()

	h := sha1.New()
	h.Write([]byte(nonce + created + pass))

	return fmt.Sprintf(`<wsse:Security xmlns:wsse="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd">
		<wsse:UsernameToken>
			<wsse:Username>%s</wsse:Username>
			<wsse:Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordDigest">%s</wsse:Password>
			<wsse:Nonce EncodingType="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-soap-message-security-1.0#Base64Binary">%s</wsse:Nonce>
			<wsu:Created xmlns:wsu="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd">%s</wsu:Created>
		</wsse:UsernameToken>
	</wsse:Security>`,
		user.Username(),
		base64.StdEncoding.EncodeToString(h.Sum(nil)),
		base64.StdEncoding.EncodeToString([]byte(nonce)),
		created)
}

func (e *Envelope) Append(args ...string) {
	for _, s := range args {
		e.buf = append(e.buf, s...)
	}
}

func (e *Envelope) Appendf(format string, args ...any) {
	e.buf = fmt.Appendf(e.buf, format, args...)
}

func (e *Envelope) Bytes() []byte {
	return append(e.buf, suffix...)
}
