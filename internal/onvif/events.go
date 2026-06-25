package onvif

import (
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AlexxIT/go2rtc/internal/api/ws"
	"github.com/AlexxIT/go2rtc/pkg/onvif"
)

// sinks maps a device name to the set of WS transports subscribed to its events.
var sinks = map[string]map[*ws.Transport]struct{}{}
var sinksMu sync.Mutex

// wsEvent is the payload pushed to WS clients for each ONVIF event.
type wsEvent struct {
	Device string            `json:"device"`
	Topic  string            `json:"topic"`
	Time   string            `json:"time,omitempty"`
	Source map[string]string `json:"source,omitempty"`
	Data   map[string]string `json:"data,omitempty"`
}

// handlerWSOnvif subscribes a WS client to a device's events:
// connect to /api/ws?src=<device> and send {"type":"onvif"}.
func handlerWSOnvif(tr *ws.Transport, _ *ws.Message) error {
	src := tr.Request.URL.Query().Get("src")
	if src == "" {
		return errors.New("onvif: src required")
	}
	if getDeviceByName(src) == nil {
		return errors.New("onvif: device not found: " + src)
	}

	addSink(src, tr)
	tr.OnClose(func() { removeSink(src, tr) })

	tr.Write(&ws.Message{Type: "onvif", Value: "subscribed:" + src})
	return nil
}

func addSink(device string, tr *ws.Transport) {
	sinksMu.Lock()
	set := sinks[device]
	if set == nil {
		set = map[*ws.Transport]struct{}{}
		sinks[device] = set
	}
	set[tr] = struct{}{}
	sinksMu.Unlock()
}

func removeSink(device string, tr *ws.Transport) {
	sinksMu.Lock()
	if set := sinks[device]; set != nil {
		delete(set, tr)
		if len(set) == 0 {
			delete(sinks, device)
		}
	}
	sinksMu.Unlock()
}

func broadcastEvent(device string, ev onvif.Event) {
	// (1) downstream ONVIF clients subscribed via the emulated event service
	emuPush(device, ev)

	// (2) native WS clients
	sinksMu.Lock()
	set := sinks[device]
	trs := make([]*ws.Transport, 0, len(set))
	for tr := range set {
		trs = append(trs, tr)
	}
	sinksMu.Unlock()

	if len(trs) == 0 {
		return
	}

	msg := &ws.Message{Type: "onvif", Value: wsEvent{
		Device: device, Topic: ev.Topic, Time: ev.Time, Source: ev.Source, Data: ev.Data,
	}}
	for _, tr := range trs {
		tr.Write(msg)
	}
}

// --- emulated ONVIF event subscriptions (broker for downstream ONVIF clients) ---
//
// When a downstream client (Home Assistant / NVR) opens a PullPoint against
// go2rtc's emulated event service, we mint an in-memory subscription that the
// always-on upstream event loop feeds via emuPush. PullMessages drains it.

const (
	emuTTL     = 5 * time.Minute // idle subscription lifetime; refreshed on pull/renew
	emuMaxSubs = 256             // global safety cap on live emulated subscriptions
)

type emuSub struct {
	device  string
	ch      chan onvif.Event
	expires time.Time
}

var (
	emuSubs   = map[string]*emuSub{}
	emuMu     sync.Mutex
	emuSeq    atomic.Uint64
	emuReaper sync.Once
)

func emuSubscribe(device string) string {
	id := strconv.FormatUint(emuSeq.Add(1), 10)
	now := time.Now()

	emuMu.Lock()
	// reclaim subscriptions from clients that vanished without Unsubscribe
	for k, s := range emuSubs {
		if now.After(s.expires) {
			delete(emuSubs, k)
		}
	}
	// safety cap against a client looping CreatePullPointSubscription: evict the
	// soonest-to-expire if still at the limit
	if len(emuSubs) >= emuMaxSubs {
		var oldestKey string
		var oldest time.Time
		for k, s := range emuSubs {
			if oldestKey == "" || s.expires.Before(oldest) {
				oldestKey, oldest = k, s.expires
			}
		}
		delete(emuSubs, oldestKey)
	}
	emuSubs[id] = &emuSub{device: device, ch: make(chan onvif.Event, 128), expires: now.Add(emuTTL)}
	emuMu.Unlock()

	emuReaper.Do(func() { go emuReap() })
	return id
}

func emuUnsubscribe(id string) {
	emuMu.Lock()
	delete(emuSubs, id)
	emuMu.Unlock()
}

// emuRenew extends a subscription's lifetime (downstream Renew or PullMessages),
// honouring the TerminationTime go2rtc advertises.
func emuRenew(id string) {
	emuMu.Lock()
	if s := emuSubs[id]; s != nil {
		s.expires = time.Now().Add(emuTTL)
	}
	emuMu.Unlock()
}

// emuReap periodically evicts expired subscriptions so orphans from crashed or
// disconnected downstream clients don't accumulate.
func emuReap() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		emuMu.Lock()
		for k, s := range emuSubs {
			if now.After(s.expires) {
				delete(emuSubs, k)
			}
		}
		emuMu.Unlock()
	}
}

func emuPush(device string, ev onvif.Event) {
	emuMu.Lock()
	for _, s := range emuSubs {
		if s.device == device {
			select {
			case s.ch <- ev:
			default: // drop if the downstream client isn't draining
			}
		}
	}
	emuMu.Unlock()
}

// emuPull blocks up to timeout for the first event, then drains any others
// already queued (long-poll semantics). Returns nil for an unknown id.
func emuPull(id string, timeout time.Duration) ([]onvif.Event, bool) {
	emuMu.Lock()
	s := emuSubs[id]
	if s != nil {
		s.expires = time.Now().Add(emuTTL) // active client: keep alive
	}
	emuMu.Unlock()
	if s == nil {
		return nil, false
	}

	var events []onvif.Event

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case ev := <-s.ch:
		events = append(events, ev)
	case <-timer.C:
		return nil, true
	}

	for len(events) < 100 {
		select {
		case ev := <-s.ch:
			events = append(events, ev)
		default:
			return events, true
		}
	}
	return events, true
}

// startEvents maintains ONE PullPoint subscription per device and fans every
// event out to all subscribed WS clients — one camera connection regardless of
// consumer count.
func startEvents(name string, dev *onvif.Device) {
	if !dev.EventsEnabled() {
		log.Debug().Msgf("[onvif] device %q events disabled", name)
		return
	}
	if !dev.HasEvents() {
		log.Debug().Msgf("[onvif] device %q has no event service", name)
		return
	}
	go eventLoop(name, dev)
}

func eventLoop(name string, dev *onvif.Device) {
	const pullTimeout = 30 * time.Second

	for retry := 0; ; {
		subRef, err := dev.CreatePullPointSubscription()
		if err != nil {
			delay := retryBackoff(retry)
			retry++
			log.Debug().Err(err).Msgf("[onvif] device %q event subscribe failed, retry in %s", name, delay)
			time.Sleep(delay)
			continue
		}

		retry = 0
		log.Info().Msgf("[onvif] device %q events subscribed", name)

		for {
			start := time.Now()
			events, err := dev.PullMessages(subRef, pullTimeout)
			if err != nil {
				log.Debug().Err(err).Msgf("[onvif] device %q pull messages failed", name)
				break // drop out to re-subscribe
			}
			for _, ev := range events {
				log.Debug().Msgf("[onvif] device %q event %s", name, ev.Topic)
				broadcastEvent(name, ev)
			}
			// Conformant cameras hold the connection ~pullTimeout. Some ignore
			// the requested hold and return an empty response instantly; floor
			// the poll rate so that case can't spin the CPU / flood the camera.
			if len(events) == 0 {
				if d := time.Second - time.Since(start); d > 0 {
					time.Sleep(d)
				}
			}
		}

		_ = dev.Unsubscribe(subRef)
		time.Sleep(time.Second)
	}
}
