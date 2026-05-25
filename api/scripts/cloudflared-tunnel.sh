#!/bin/bash
# Cloudflare Quick Tunnel wrapper for PenguinYieldVault API
# This script starts a cloudflared quick tunnel and extracts the assigned URL
# The URL is written to /home/ark009770/PenguinYieldVault/api/data/tunnel-url.txt

TUNNEL_URL_FILE="/home/ark009770/PenguinYieldVault/api/data/tunnel-url.txt"
LOG_FILE="/home/ark009770/PenguinYieldVault/api/data/cloudflared.log"

# Clear old URL file
echo "" > "$TUNNEL_URL_FILE"

# Start cloudflared and tee output so we can parse the URL
cloudflared tunnel --url http://localhost:8080 --no-autoupdate 2>&1 | while IFS= read -r line; do
    echo "$line" >> "$LOG_FILE"
    # Extract the trycloudflare.com URL from the output
    if echo "$line" | grep -qo 'https://[a-zA-Z0-9-]*\.trycloudflare\.com'; then
        URL=$(echo "$line" | grep -o 'https://[a-zA-Z0-9-]*\.trycloudflare\.com')
        echo "$URL" > "$TUNNEL_URL_FILE"
        echo "[tunnel-wrapper] Tunnel URL saved: $URL" >> "$LOG_FILE"
    fi
done
