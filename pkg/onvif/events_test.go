package onvif

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseEvents(t *testing.T) {
	// realistic Hikvision/Dahua-style PullMessagesResponse with one motion event
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<env:Envelope xmlns:env="http://www.w3.org/2003/05/soap-envelope" xmlns:tt="http://www.onvif.org/ver10/schema" xmlns:wsnt="http://docs.oasis-open.org/wsn/b-2" xmlns:tev="http://www.onvif.org/ver10/events/wsdl">
  <env:Body>
    <tev:PullMessagesResponse>
      <tev:CurrentTime>2026-06-25T18:00:00Z</tev:CurrentTime>
      <tev:TerminationTime>2026-06-25T19:00:00Z</tev:TerminationTime>
      <wsnt:NotificationMessage>
        <wsnt:Topic Dialect="http://www.onvif.org/ver10/tev/topicExpression/ConcreteSet">tns1:VideoSource/MotionAlarm</wsnt:Topic>
        <wsnt:Message>
          <tt:Message UtcTime="2026-06-25T18:00:01Z" PropertyOperation="Changed">
            <tt:Source>
              <tt:SimpleItem Name="Source" Value="VideoSourceToken"/>
            </tt:Source>
            <tt:Data>
              <tt:SimpleItem Name="State" Value="true"/>
            </tt:Data>
          </tt:Message>
        </wsnt:Message>
      </wsnt:NotificationMessage>
    </tev:PullMessagesResponse>
  </env:Body>
</env:Envelope>`

	events := parseEvents([]byte(xml))
	require.Len(t, events, 1)

	ev := events[0]
	require.Equal(t, "tns1:VideoSource/MotionAlarm", ev.Topic)
	require.Equal(t, "2026-06-25T18:00:01Z", ev.Time)
	require.Equal(t, "VideoSourceToken", ev.Source["Source"])
	require.Equal(t, "true", ev.Data["State"])
}

func TestParseEventsEmpty(t *testing.T) {
	// PullMessages timed out with no events
	xml := `<env:Envelope xmlns:env="http://www.w3.org/2003/05/soap-envelope" xmlns:tev="http://www.onvif.org/ver10/events/wsdl"><env:Body><tev:PullMessagesResponse><tev:CurrentTime>2026-06-25T18:00:00Z</tev:CurrentTime></tev:PullMessagesResponse></env:Body></env:Envelope>`
	require.Empty(t, parseEvents([]byte(xml)))
}
