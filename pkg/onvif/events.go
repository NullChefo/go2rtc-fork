package onvif

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// WS-Addressing action URIs for the WS-BaseNotification PullPoint flow.
const (
	actionCreatePullPoint = "http://www.onvif.org/ver10/events/wsdl/EventPortType/CreatePullPointSubscriptionRequest"
	actionPullMessages    = "http://www.onvif.org/ver10/events/wsdl/PullPointSubscription/PullMessagesRequest"
	actionRenew           = "http://docs.oasis-open.org/wsn/bw-2/SubscriptionManager/RenewRequest"
	actionUnsubscribe     = "http://docs.oasis-open.org/wsn/bw-2/SubscriptionManager/UnsubscribeRequest"
)

// Event is a normalized ONVIF notification message.
type Event struct {
	Topic  string            `json:"topic"`
	Time   string            `json:"time,omitempty"`
	Source map[string]string `json:"source,omitempty"`
	Data   map[string]string `json:"data,omitempty"`
}

// CreatePullPointSubscription opens an event PullPoint and returns the
// subscription-manager reference URL used for PullMessages/Renew/Unsubscribe.
func (d *Device) CreatePullPointSubscription() (string, error) {
	if err := d.Connect(); err != nil {
		return "", err
	}
	if d.eventsURL == "" {
		return "", errors.New("onvif: no events service")
	}

	b, err := d.requestEvent(d.eventsURL, actionCreatePullPoint,
		`<tev:CreatePullPointSubscription><tev:InitialTerminationTime>PT1H</tev:InitialTerminationTime></tev:CreatePullPointSubscription>`,
		15*time.Second)
	if err != nil {
		return "", err
	}

	ref := strings.TrimSpace(FindTagValue(b, "Address"))
	if ref == "" {
		ref = d.eventsURL // some cameras pull directly from the events service
	}
	return d.fixURL(ref), nil
}

// PullMessages long-polls for events. The HTTP deadline exceeds the camera hold
// time so the camera, not the client, decides when the call returns.
func (d *Device) PullMessages(subRef string, timeout time.Duration) ([]Event, error) {
	body := fmt.Sprintf(
		`<tev:PullMessages><tev:Timeout>PT%dS</tev:Timeout><tev:MessageLimit>100</tev:MessageLimit></tev:PullMessages>`,
		int(timeout.Seconds()),
	)
	b, err := d.requestEvent(subRef, actionPullMessages, body, timeout+10*time.Second)
	if err != nil {
		return nil, err
	}
	return parseEvents(b), nil
}

func (d *Device) RenewSubscription(subRef string) error {
	_, err := d.requestEvent(subRef, actionRenew,
		`<wsnt:Renew><wsnt:TerminationTime>PT1H</wsnt:TerminationTime></wsnt:Renew>`, 15*time.Second)
	return err
}

func (d *Device) Unsubscribe(subRef string) error {
	_, err := d.requestEvent(subRef, actionUnsubscribe, `<wsnt:Unsubscribe/>`, 10*time.Second)
	return err
}

// requestEvent posts a WS-Addressing + WS-Security SOAP request governed by a
// context deadline. The event client has no global timeout, so the long-poll
// PullMessages call can exceed the 15s control-call timeout.
func (d *Device) requestEvent(serviceURL, action, body string, timeout time.Duration) ([]byte, error) {
	if serviceURL == "" {
		return nil, errors.New("onvif: no events service")
	}

	e := NewEnvelopeWithAction(d.url.User, action, serviceURL)
	e.Append(body)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serviceURL, bytes.NewReader(e.Bytes()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/soap+xml;charset=utf-8")

	res, err := d.eventClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, errors.New("onvif: events status " + res.Status)
	}
	return io.ReadAll(res.Body)
}

// fixURL rewrites a camera-reported subscription URL onto the configured host
// when the camera returns an unusable address (e.g. 0.0.0.0); otherwise it is
// used verbatim (the ?Idx= token, if any, is preserved).
func (d *Device) fixURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	if u.Host == "" || strings.HasPrefix(u.Host, "0.0.0.0") {
		u.Scheme = "http"
		u.Host = d.url.Host
	}
	return u.String()
}

var (
	reNotification = regexp.MustCompile(`(?s)<(?:\w+:)?NotificationMessage\b.*?</(?:\w+:)?NotificationMessage>`)
	reTopic        = regexp.MustCompile(`(?s)<(?:\w+:)?Topic\b[^>]*>([^<]+)`)
	reUtcTime      = regexp.MustCompile(`UtcTime="([^"]+)"`)
	reSourceBlock  = regexp.MustCompile(`(?s)<(?:\w+:)?Source\b[^>]*>(.*?)</(?:\w+:)?Source>`)
	reDataBlock    = regexp.MustCompile(`(?s)<(?:\w+:)?Data\b[^>]*>(.*?)</(?:\w+:)?Data>`)
	reSimpleItem   = regexp.MustCompile(`<(?:\w+:)?SimpleItem\b[^>]*>`)
	reAttrName     = regexp.MustCompile(`\bName="([^"]*)"`)
	reAttrValue    = regexp.MustCompile(`\bValue="([^"]*)"`)
)

// parseEvents extracts NotificationMessage blocks into normalized Events. It is
// regex-based (ONVIF event XML varies wildly across vendors) and skips anything
// without a topic.
func parseEvents(b []byte) []Event {
	var events []Event
	for _, block := range reNotification.FindAll(b, -1) {
		var ev Event
		if m := reTopic.FindSubmatch(block); m != nil {
			ev.Topic = strings.TrimSpace(string(m[1]))
		}
		if m := reUtcTime.FindSubmatch(block); m != nil {
			ev.Time = string(m[1])
		}
		if m := reSourceBlock.FindSubmatch(block); m != nil {
			ev.Source = parseSimpleItems(m[1])
		}
		if m := reDataBlock.FindSubmatch(block); m != nil {
			ev.Data = parseSimpleItems(m[1])
		}
		if ev.Topic != "" {
			events = append(events, ev)
		}
	}
	return events
}

func parseSimpleItems(b []byte) map[string]string {
	var items map[string]string
	for _, item := range reSimpleItem.FindAll(b, -1) {
		nm := reAttrName.FindSubmatch(item)
		if nm == nil {
			continue
		}
		var val string
		if vm := reAttrValue.FindSubmatch(item); vm != nil {
			val = string(vm[1])
		}
		if items == nil {
			items = map[string]string{}
		}
		items[string(nm[1])] = val
	}
	return items
}
