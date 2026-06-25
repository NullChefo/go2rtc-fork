package onvif

import "time"

// Operation local-names (from GetRequestAction) for the emulated event service.
const (
	EventsCreatePullPointSubscription = "CreatePullPointSubscription"
	EventsPullMessages                = "PullMessages"
	EventsRenew                       = "Renew"
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

// DeviceProfilesResponse / DeviceProfileResponse expose the device's go2rtc
// stream names as ONVIF profile tokens, embedding a PTZConfiguration when the
// camera has PTZ so downstream clients enable PTZ controls.
func DeviceProfilesResponse(names []string, ptz bool) []byte {
	e := NewEnvelope()
	e.Append(`<trt:GetProfilesResponse>`)
	for _, name := range names {
		appendDeviceProfile(e, "Profiles", name, ptz)
	}
	e.Append(`</trt:GetProfilesResponse>`)
	return e.Bytes()
}

func DeviceProfileResponse(name string, ptz bool) []byte {
	e := NewEnvelope()
	e.Append(`<trt:GetProfileResponse>`)
	appendDeviceProfile(e, "Profile", name, ptz)
	e.Append(`</trt:GetProfileResponse>`)
	return e.Bytes()
}

func appendDeviceProfile(e *Envelope, tag, name string, ptz bool) {
	en := escapeXML(name)
	e.Appendf(`<trt:%s token="%s" fixed="true"><tt:Name>%s</tt:Name>`, tag, en, en)
	appendVideoSourceConfiguration(e, "VideoSourceConfiguration", name) // escapes internally
	appendVideoEncoderConfiguration(e, "VideoEncoderConfiguration")
	if ptz {
		e.Append(`<tt:PTZConfiguration token="ptz0"><tt:Name>PTZ</tt:Name><tt:UseCount>1</tt:UseCount><tt:NodeToken>ptz0</tt:NodeToken></tt:PTZConfiguration>`)
	}
	e.Appendf(`</trt:%s>`, tag)
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
