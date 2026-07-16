package onvif

import (
	"strconv"
	"time"
)

// Operation local-names (from GetRequestAction) for the emulated event service.
const (
	EventsCreatePullPointSubscription = "CreatePullPointSubscription"
	EventsPullMessages                = "PullMessages"
	EventsRenew                       = "Renew"
	EventsSetSynchronizationPoint     = "SetSynchronizationPoint"
	EventsSubscribe                   = "Subscribe"
	EventsUnsubscribe                 = "Unsubscribe"
	EventsGetEventProperties          = "GetEventProperties"
)

func soapTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

// DeviceCapabilities builds a GetCapabilities response for a per-device emulated
// ONVIF device, advertising Device/Media/Events/Imaging (+PTZ) under prefix
// (e.g. "/onvif/frontdoor") so clients address subsequent calls per device.
func DeviceCapabilities(host, prefix string, ptz bool) []byte {
	host, prefix = escapeXML(host), escapeXML(prefix)
	e := NewEnvelope()
	e.Append(`<tds:GetCapabilitiesResponse><tds:Capabilities>`)
	e.Appendf(`<tt:Device><tt:XAddr>http://%s%s/device_service</tt:XAddr></tt:Device>`, host, prefix)
	e.Appendf(`<tt:Events><tt:XAddr>http://%s%s/event_service</tt:XAddr><tt:WSSubscriptionPolicySupport>true</tt:WSSubscriptionPolicySupport><tt:WSPullPointSupport>true</tt:WSPullPointSupport></tt:Events>`, host, prefix)
	e.Appendf(`<tt:Imaging><tt:XAddr>http://%s%s/imaging_service</tt:XAddr></tt:Imaging>`, host, prefix)
	e.Appendf(`<tt:Media><tt:XAddr>http://%s%s/media_service</tt:XAddr><tt:StreamingCapabilities><tt:RTPMulticast>false</tt:RTPMulticast><tt:RTP_TCP>false</tt:RTP_TCP><tt:RTP_RTSP_TCP>true</tt:RTP_RTSP_TCP></tt:StreamingCapabilities></tt:Media>`, host, prefix)
	if ptz {
		e.Appendf(`<tt:PTZ><tt:XAddr>http://%s%s/ptz_service</tt:XAddr></tt:PTZ>`, host, prefix)
	}
	e.Append(`</tds:Capabilities></tds:GetCapabilitiesResponse>`)
	return e.Bytes()
}

func DeviceServices(host, prefix string, ptz bool) []byte {
	host, prefix = escapeXML(host), escapeXML(prefix)
	e := NewEnvelope()
	e.Append(`<tds:GetServicesResponse>`)
	svc := func(ns, path string) {
		e.Appendf(`<tds:Service><tds:Namespace>%s</tds:Namespace><tds:XAddr>http://%s%s/%s</tds:XAddr><tds:Version><tt:Major>2</tt:Major><tt:Minor>5</tt:Minor></tds:Version></tds:Service>`, ns, host, prefix, path)
	}
	svc("http://www.onvif.org/ver10/device/wsdl", "device_service")
	svc("http://www.onvif.org/ver10/media/wsdl", "media_service")
	svc("http://www.onvif.org/ver10/events/wsdl", "event_service")
	svc("http://www.onvif.org/ver20/imaging/wsdl", "imaging_service")
	if ptz {
		svc("http://www.onvif.org/ver20/ptz/wsdl", "ptz_service")
	}
	e.Append(`</tds:GetServicesResponse>`)
	return e.Bytes()
}

// ProfileInfo is the per-profile data the emulated media service advertises.
// Token is the exposed token (= the go2rtc stream name a client requests);
// Width/Height/Codec describe what the client will actually receive (after any
// transcode). Rate-control values are copied from the source profile. In
// particular, BitrateLimit is only an estimate when the stream is transcoded;
// advertising it does not configure or measure the transcoded encoder.
type ProfileInfo struct {
	Token            string
	Width            int
	Height           int
	Codec            string // ONVIF Encoding: H264 / H265 / JPEG
	Audio            string // ONVIF AudioEncoding: G711 / AAC (empty = G711)
	FrameRateLimit   int
	EncodingInterval int
	BitrateLimit     int // upstream kbps; estimate when transcoding
}

func (p ProfileInfo) audioEnc() string {
	if p.Audio != "" {
		return p.Audio
	}
	return "G711"
}

func (p ProfileInfo) wh() (int, int) {
	if p.Width > 0 && p.Height > 0 {
		return p.Width, p.Height
	}
	return 1920, 1080 // fallback when the camera didn't report a resolution
}

func (p ProfileInfo) enc() string {
	if p.Codec != "" {
		return p.Codec
	}
	return "H264"
}

func (p ProfileInfo) frameRateLimit() int {
	if p.FrameRateLimit > 0 {
		return p.FrameRateLimit
	}
	return defaultFrameRateLimit
}

// deviceSource models the ONE physical video/audio source shared by all of a
// device's profiles. Real cameras (and XMEye NVR channel-matching in
// particular) expect main+sub profiles to reference the SAME VideoSource; a
// device advertising one source per profile looks like a multi-channel encoder
// and XM NVRs never map it to a channel (no GetStreamUri, "Not Logged In").
type deviceSource struct {
	W, H  int // sensor resolution = main (first) profile's resolution
	Count int // number of profiles sharing the source (UseCount)
}

// MainSource derives the shared source from the profile list (first = main).
func MainSource(profiles []ProfileInfo) deviceSource {
	src := deviceSource{W: 1920, H: 1080, Count: len(profiles)}
	if len(profiles) > 0 {
		src.W, src.H = profiles[0].wh()
	}
	if src.Count == 0 {
		src.Count = 1
	}
	return src
}

// profileName mimics XM camera naming: NVRs of the same family match
// mainStream/subStream when assigning channel streams.
func profileName(idx int) string {
	switch idx {
	case 0:
		return "mainStream"
	case 1:
		return "subStream"
	default:
		return "extraStream" + itoa(idx)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

// DeviceProfilesResponse / DeviceProfileResponse expose the device's go2rtc
// stream names as ONVIF profile tokens, with accurate resolution + codec, one
// shared Video/AudioSource across profiles (real-camera shape) and a
// PTZConfiguration when the camera has PTZ.
func DeviceProfilesResponse(profiles []ProfileInfo, ptz bool) []byte {
	src := MainSource(profiles)
	e := NewEnvelope()
	e.Append(`<trt:GetProfilesResponse>`)
	for i, p := range profiles {
		appendDeviceProfile(e, "Profiles", p, i, src, ptz)
	}
	e.Append(`</trt:GetProfilesResponse>`)
	return e.Bytes()
}

func DeviceProfileResponse(p ProfileInfo, idx int, src deviceSource, ptz bool) []byte {
	e := NewEnvelope()
	e.Append(`<trt:GetProfileResponse>`)
	appendDeviceProfile(e, "Profile", p, idx, src, ptz)
	e.Append(`</trt:GetProfileResponse>`)
	return e.Bytes()
}

func appendDeviceProfile(e *Envelope, tag string, p ProfileInfo, idx int, src deviceSource, ptz bool) {
	tok := escapeXML(p.Token)
	e.Appendf(`<trt:%s token="%s" fixed="true"><tt:Name>%s</tt:Name>`, tag, tok, profileName(idx))
	appendDeviceVSC(e, "VideoSourceConfiguration", src)
	// schema order: AudioSourceConfiguration between VSC and VEC. Strict
	// (gSOAP-based) clients like XMEye NVRs require the audio configurations
	// and reject profiles without them. One shared audio source, like a real
	// camera.
	e.Appendf(`<tt:AudioSourceConfiguration token="A_SRC_CFG_000"><tt:Name>A_SRC_CFG_000</tt:Name><tt:UseCount>%d</tt:UseCount><tt:SourceToken>AudioSourceToken</tt:SourceToken></tt:AudioSourceConfiguration>`, src.Count)
	appendDeviceVEC(e, "VideoEncoderConfiguration", p, idx)
	e.Appendf(`<tt:AudioEncoderConfiguration token="aec_%s"><tt:Name>A_ENC_%03d</tt:Name><tt:UseCount>1</tt:UseCount><tt:Encoding>%s</tt:Encoding><tt:Bitrate>128</tt:Bitrate><tt:SampleRate>8</tt:SampleRate>%s<tt:SessionTimeout>PT10S</tt:SessionTimeout></tt:AudioEncoderConfiguration>`, tok, idx, p.audioEnc(), multicastBlock)
	if ptz {
		e.Append(`<tt:PTZConfiguration token="ptz0"><tt:Name>PTZ</tt:Name><tt:UseCount>1</tt:UseCount><tt:NodeToken>ptz0</tt:NodeToken></tt:PTZConfiguration>`)
	}
	e.Appendf(`</trt:%s>`, tag)
}

// multicastBlock is schema-mandatory inside Video/AudioEncoderConfiguration;
// strict parsers (XMEye/gSOAP) fail the whole document without it.
const multicastBlock = `<tt:Multicast><tt:Address><tt:Type>IPv4</tt:Type><tt:IPv4Address>0.0.0.0</tt:IPv4Address></tt:Address><tt:Port>0</tt:Port><tt:TTL>1</tt:TTL><tt:AutoStart>false</tt:AutoStart></tt:Multicast>`

// appendDeviceVSC emits the ONE shared VideoSourceConfiguration (token/source
// V_SRC_000, sensor = main resolution), mirroring real XM cameras.
func appendDeviceVSC(e *Envelope, tag string, src deviceSource) {
	// UseCount is schema-mandatory (Name, UseCount, SourceToken, Bounds)
	e.Appendf(`<tt:%s token="V_SRC_000"><tt:Name>V_SRC_CFG_000</tt:Name><tt:UseCount>%d</tt:UseCount><tt:SourceToken>V_SRC_000</tt:SourceToken><tt:Bounds x="0" y="0" width="%d" height="%d"></tt:Bounds></tt:%s>`, tag, src.Count, src.W, src.H, tag)
}

func appendDeviceVEC(e *Envelope, tag string, p ProfileInfo, idx int) {
	w, h := p.wh()
	codec := p.enc()
	// unique token per profile (a multi-profile device exposes several VECs);
	// compatibility metadata stays within common NVR channel caps
	// real XM cameras use the profile token as the VEC token too (e.g. "000")
	e.Appendf(`<tt:%s token="%s"><tt:Name>V_ENC_%03d</tt:Name><tt:UseCount>1</tt:UseCount><tt:Encoding>%s</tt:Encoding><tt:Resolution><tt:Width>%d</tt:Width><tt:Height>%d</tt:Height></tt:Resolution><tt:Quality>0</tt:Quality>`, tag, escapeXML(p.Token), idx, codec, w, h)
	appendRateControl(e, p.FrameRateLimit, p.EncodingInterval, p.BitrateLimit)
	switch codec {
	case "H265":
		e.Append(`<tt:H265><tt:GovLength>10</tt:GovLength><tt:H265Profile>Main</tt:H265Profile></tt:H265>`)
	case "JPEG":
		// no codec-specific options block for JPEG
	default:
		e.Append(`<tt:H264><tt:GovLength>10</tt:GovLength><tt:H264Profile>Main</tt:H264Profile></tt:H264>`)
	}
	e.Append(multicastBlock)
	e.Appendf(`<tt:SessionTimeout>PT10S</tt:SessionTimeout></tt:%s>`, tag)
}

// DeviceVideoSourcesResponse advertises the ONE physical video source (like a
// real camera), not one per profile.
func DeviceVideoSourcesResponse(profiles []ProfileInfo) []byte {
	src := MainSource(profiles)
	e := NewEnvelope()
	e.Append(`<trt:GetVideoSourcesResponse>`)
	frameRateLimit := defaultFrameRateLimit
	if len(profiles) > 0 {
		frameRateLimit = profiles[0].frameRateLimit()
	}
	e.Appendf(`<trt:VideoSources token="V_SRC_000"><tt:Framerate>%d.000000</tt:Framerate><tt:Resolution><tt:Width>%d</tt:Width><tt:Height>%d</tt:Height></tt:Resolution></trt:VideoSources>`, frameRateLimit, src.W, src.H)
	e.Append(`</trt:GetVideoSourcesResponse>`)
	return e.Bytes()
}

func DeviceVideoSourceConfigurationsResponse(profiles []ProfileInfo) []byte {
	e := NewEnvelope()
	e.Append(`<trt:GetVideoSourceConfigurationsResponse>`)
	appendDeviceVSC(e, "Configurations", MainSource(profiles))
	e.Append(`</trt:GetVideoSourceConfigurationsResponse>`)
	return e.Bytes()
}

func DeviceVideoSourceConfigurationResponse(profiles []ProfileInfo) []byte {
	e := NewEnvelope()
	e.Append(`<trt:GetVideoSourceConfigurationResponse>`)
	appendDeviceVSC(e, "Configuration", MainSource(profiles))
	e.Append(`</trt:GetVideoSourceConfigurationResponse>`)
	return e.Bytes()
}

func DeviceVideoEncoderConfigurationsResponse(profiles []ProfileInfo) []byte {
	e := NewEnvelope()
	e.Append(`<trt:GetVideoEncoderConfigurationsResponse>`)
	for i, p := range profiles {
		appendDeviceVEC(e, "Configurations", p, i)
	}
	e.Append(`</trt:GetVideoEncoderConfigurationsResponse>`)
	return e.Bytes()
}

func DeviceVideoEncoderConfigurationResponse(p ProfileInfo, idx int) []byte {
	e := NewEnvelope()
	e.Append(`<trt:GetVideoEncoderConfigurationResponse>`)
	appendDeviceVEC(e, "Configuration", p, idx)
	e.Append(`</trt:GetVideoEncoderConfigurationResponse>`)
	return e.Bytes()
}

// EventServiceCapabilitiesResponse answers GetServiceCapabilities on the event
// service with the correct tev namespace (the device-service static response is
// a media-namespace payload and would be semantically wrong here).
func EventServiceCapabilitiesResponse() []byte {
	e := NewEnvelope()
	e.Append(`<tev:GetServiceCapabilitiesResponse><tev:Capabilities WSSubscriptionPolicySupport="true" WSPullPointSupport="true" WSPausableSubscriptionManagerInterfaceSupport="false" MaxNotificationProducers="10" MaxPullPoints="10" PersistentNotificationStorage="false"/></tev:GetServiceCapabilitiesResponse>`)
	return e.Bytes()
}

func GetEventPropertiesResponse() []byte {
	e := NewEnvelope()
	e.Append(`<tev:GetEventPropertiesResponse>` +
		`<tev:TopicNamespaceLocation>http://www.onvif.org/onvif/ver10/topics/topicns.xml</tev:TopicNamespaceLocation>` +
		`<wsnt:FixedTopicSet>true</wsnt:FixedTopicSet>` +
		`<wstop:TopicSet xmlns:wstop="http://docs.oasis-open.org/wsn/t-1" xmlns:tns1="http://www.onvif.org/ver10/topics">` +
		`<tns1:VideoSource><MotionAlarm wstop:topic="true"><tt:MessageDescription IsProperty="true">` +
		`<tt:Source><tt:SimpleItemDescription Name="Source" Type="tt:ReferenceToken"/></tt:Source>` +
		`<tt:Data><tt:SimpleItemDescription Name="State" Type="xs:boolean"/></tt:Data>` +
		`</tt:MessageDescription></MotionAlarm></tns1:VideoSource>` +
		`</wstop:TopicSet>` +
		`<wsnt:TopicExpressionDialect>http://www.onvif.org/ver10/tev/topicExpression/ConcreteSet</wsnt:TopicExpressionDialect>` +
		`<wsnt:MessageContentFilterDialect>http://www.onvif.org/ver10/tev/messageContentFilter/ItemFilter</wsnt:MessageContentFilterDialect>` +
		`</tev:GetEventPropertiesResponse>`)
	return e.Bytes()
}

func CreatePullPointSubscriptionResponse(address string, now time.Time) []byte {
	e := NewEnvelope()
	e.Appendf(`<tev:CreatePullPointSubscriptionResponse>`+
		`<tev:SubscriptionReference><wsa:Address>%s</wsa:Address></tev:SubscriptionReference>`+
		`<wsnt:CurrentTime>%s</wsnt:CurrentTime><wsnt:TerminationTime>%s</wsnt:TerminationTime>`+
		`</tev:CreatePullPointSubscriptionResponse>`,
		escapeXML(address), soapTime(now), soapTime(now.Add(time.Hour)))
	return e.Bytes()
}

// SubscribeResponse accepts WS-Notification push subscriptions for compatibility
// with NVRs that probe both push and PullPoint event modes. The returned
// subscription-manager address supports Renew/Unsubscribe; event delivery
// continues through the existing ONVIF PullPoint broker.
func SubscribeResponse(address string, now time.Time) []byte {
	e := NewEnvelope()
	e.Appendf(`<wsnt:SubscribeResponse>`+
		`<wsnt:SubscriptionReference><wsa:Address>%s</wsa:Address></wsnt:SubscriptionReference>`+
		`<wsnt:CurrentTime>%s</wsnt:CurrentTime><wsnt:TerminationTime>%s</wsnt:TerminationTime>`+
		`</wsnt:SubscribeResponse>`,
		escapeXML(address), soapTime(now), soapTime(now.Add(time.Hour)))
	return e.Bytes()
}

// SetSynchronizationPointResponse acknowledges the optional ONVIF event
// synchronization request. The virtual device has no retained property state to
// replay, so successful no-op semantics are appropriate.
func SetSynchronizationPointResponse() []byte {
	e := NewEnvelope()
	e.Append(`<tev:SetSynchronizationPointResponse/>`)
	return e.Bytes()
}

func PullMessagesResponse(events []Event, now time.Time) []byte {
	e := NewEnvelope()
	e.Appendf(`<tev:PullMessagesResponse><tev:CurrentTime>%s</tev:CurrentTime><tev:TerminationTime>%s</tev:TerminationTime>`,
		soapTime(now), soapTime(now.Add(time.Hour)))
	for _, ev := range events {
		t := ev.Time
		if t == "" {
			t = soapTime(now)
		}
		e.Appendf(`<wsnt:NotificationMessage><wsnt:Topic Dialect="http://www.onvif.org/ver10/tev/topicExpression/ConcreteSet" xmlns:tns1="http://www.onvif.org/ver10/topics">%s</wsnt:Topic><wsnt:Message><tt:Message UtcTime="%s">`, escapeXML(ev.Topic), escapeXML(t))
		appendSimpleItems(e, "Source", ev.Source)
		appendSimpleItems(e, "Data", ev.Data)
		e.Append(`</tt:Message></wsnt:Message></wsnt:NotificationMessage>`)
	}
	e.Append(`</tev:PullMessagesResponse>`)
	return e.Bytes()
}

func appendSimpleItems(e *Envelope, tag string, items map[string]string) {
	if len(items) == 0 {
		return
	}
	e.Appendf(`<tt:%s>`, tag)
	for k, v := range items {
		e.Appendf(`<tt:SimpleItem Name="%s" Value="%s"/>`, escapeXML(k), escapeXML(v))
	}
	e.Appendf(`</tt:%s>`, tag)
}

func RenewResponse(now time.Time) []byte {
	e := NewEnvelope()
	e.Appendf(`<wsnt:RenewResponse><wsnt:CurrentTime>%s</wsnt:CurrentTime><wsnt:TerminationTime>%s</wsnt:TerminationTime></wsnt:RenewResponse>`,
		soapTime(now), soapTime(now.Add(time.Hour)))
	return e.Bytes()
}

func UnsubscribeResponse() []byte {
	e := NewEnvelope()
	e.Append(`<wsnt:UnsubscribeResponse/>`)
	return e.Bytes()
}
