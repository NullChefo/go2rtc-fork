# Build

```bash
# PAT with write:packages,read:packages  → https://github.com/settings/tokens (classic)
echo "$CR_PAT" | docker login ghcr.io -u NullChefo --password-stdin

docker buildx build \
  --platform linux/amd64 \
  -f docker/Dockerfile \
  -t ghcr.io/nullchefo/go2rtc-onvif:latest \
  --push .
```


# Run

```bash

docker run -d --name go2rtc-onvif --restart unless-stopped \
  --network host \
  -v ./go2rtc.yaml:/config/go2rtc.yaml \
  ghcr.io/nullchefo/go2rtc-onvif:latest

```