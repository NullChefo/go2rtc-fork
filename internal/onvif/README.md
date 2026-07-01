# ONVIF

## ONVIF Client

[`new in v1.5.0`](https://github.com/AlexxIT/go2rtc/releases/tag/v1.5.0)

The source is not very useful if you already know RTSP and snapshot links for your camera. But it can be useful if you don't.

**WebUI > Add** webpage supports ONVIF autodiscovery. Your server must be on the same subnet as the camera. If you use Docker, you must use "network host".

```yaml
streams:
  dahua1: onvif://admin:password@192.168.1.123
  reolink1: onvif://admin:password@192.168.1.123:8000
  tapo1: onvif://admin:password@192.168.1.123:2020
```

## ONVIF Server

A regular camera has a single video source (`GetVideoSources`) and two profiles (`GetProfiles`).

Go2rtc has one video source and one profile per stream.

## Tested clients

Go2rtc works as ONVIF server:

- Happytime onvif client (windows)
- Home Assistant ONVIF integration (linux)
- Onvier (android)
- ONVIF Device Manager (windows)

PS. Supports only TCP transport for RTSP protocol. UDP and HTTP transports - unsupported yet.

## Tested cameras

Go2rtc works as ONVIF client:

- Dahua IPC-K42
- OpenIPC
- Reolink RLC-520A
- TP-Link Tapo TC60

## ONVIF Devices (persistent cameras)

`new in this fork` — define a camera **once** under `onvif.devices` and go2rtc keeps **one persistent connection per consumed profile**, no matter how many clients pull from go2rtc (RTSP/WebRTC/HLS/MP4/snapshots/NVRs all share it). Designed for bandwidth-limited (WiFi) cameras.

```yaml
onvif:
  devices:
    balcony:
      url: 'onvif://admin:password@192.168.1.123:8899'  # required
      name: 'Balcony'          # optional display name
      listen: ':8901'          # serve THIS camera as its own ONVIF device on this port
      profiles: ['000', '001'] # profile-token allowlist (default: all)
      prefetch: ['001']        # keep these profiles always connected/warm ('*' = all)
      snapshot: stream         # stream (keyframe, default) | native (camera JPEG)
      events: true             # camera event subscription (default true)
      transcode:               # optional re-encode (e.g. UniFi Protect wants H264+AAC)
        video: h264            # h264 | h265 | '' (passthrough)
        audio: aac             # aac | opus | pcma | '' (passthrough)
        hardware: vaapi        # vaapi (Intel/AMD, needs /dev/dri) | '' (software)
```

What you get per device:

- **Streams**: first profile = `balcony`, others `balcony_1`, … (with `transcode`, the raw camera stream is `balcony_src` etc. and the exposed name serves the transcoded output).
- **Virtual ONVIF camera** on `listen` — point any NVR at `onvif://any:any@<go2rtc>:8901` (credentials are not validated; firewall the port or keep it on a trusted LAN — PTZ/imaging are proxied to the real camera with its stored admin credentials). Announced via WS-Discovery (UDP 3702), so NVR scans find it.
- **Snapshots**: `GET /api/onvif/snapshot?src=balcony[&profile=<token>][&cache=1s]` (302 to `/api/frame.jpeg` in `stream` mode).
- **Events**: WebSocket `/api/ws?src=balcony` + `{"type":"onvif"}`; also brokered to downstream ONVIF PullPoint subscribers of the virtual camera. One upstream subscription per device.
- **PTZ**: `GET /api/onvif/ptz?src=balcony&action=continuous|stop|absolute|relative|preset&pan=&tilt=&zoom=&preset=`; presets at `/api/onvif/presets?src=balcony`. Also proxied through the virtual camera's PTZ service.
- **Imaging**: `GET /api/onvif/imaging?src=balcony&token=<videoSourceToken>`; also proxied via the virtual camera.
- **Anything else**: `POST /api/onvif/soap?src=balcony&service=device|media|imaging|events|ptz` forwards raw SOAP re-signed with the camera credentials (full admin access — protect the API with `api: username/password`).
- **Info**: `GET /api/onvif/info?src=balcony` (capabilities + profile→stream map).

VAAPI note: hardware transcode uses `/dev/dri/renderD128` by default; override with

```yaml
ffmpeg:
  vaapi_device: /dev/dri/renderD129
```

UniFi Protect recipe (Intel host, `/dev/dri` mapped into the container):

```yaml
onvif:
  devices:
    balcony:
      url: 'onvif://admin:password@192.168.1.123:8899'
      listen: ':8901'
      profiles: ['000', '001']
      transcode: { video: h264, audio: aac, hardware: vaapi }
      prefetch: ['001']   # each prefetched profile = one always-on camera connection
```

Then adopt `<go2rtc-ip>:8901` in Protect (any username/password). See `DOCKER.md` for the container build/run.
