#!/usr/bin/env bash

# Reguant Auto-Installer Script
# For Ubuntu and Debian systems.

set -euo pipefail

# Output styling
RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}===============================================${NC}"
echo -e "${GREEN}       Reguant PaaS Installer Starting        ${NC}"
echo -e "${BLUE}===============================================${NC}"

# Ensure running as root
if [ "$EUID" -ne 0 ]; then
  echo -e "${RED}Error: Please run this installer as root (e.g. sudo bash install.sh)${NC}"
  exit 1
fi

# 1. Update Packages & Dependencies
echo -e "\n${BLUE}[1/5] Installing core server dependencies...${NC}"
apt-get update
apt-get install -y curl git wget build-essential Nginx certbot python3-certbot-nginx jq

# 2. Check & Install Docker
if ! command -v docker &> /dev/null; then
  echo -e "${BLUE}Installing Docker Engine...${NC}"
  curl -fsSL https://get.docker.com -o get-docker.sh
  sh get-docker.sh
  rm get-docker.sh
else
  echo -e "${GREEN}Docker is already installed.${NC}"
fi

# 3. Check & Install Go (Golang)
if ! command -v go &> /dev/null; then
  echo -e "${BLUE}Golang not found. Installing latest stable Go version...${NC}"
  GO_VERSION="1.22.0"
  wget "https://golang.org/dl/go${GO_VERSION}.linux-amd64.tar.gz" -O go.tar.gz
  rm -rf /usr/local/go
  tar -C /usr/local -xzf go.tar.gz
  rm go.tar.gz
  export PATH=$PATH:/usr/local/go
  ln -sf /usr/local/go/bin/go /usr/bin/go
  echo -e "${GREEN}Go version $(go version) installed successfully.${NC}"
else
  echo -e "${GREEN}Go is already installed: $(go version)${NC}"
fi

# 4. Prepare Reguant Directory Layout
echo -e "\n${BLUE}[2/5] Creating Reguant directories...${NC}"
mkdir -p /var/lib/reguant/apps
mkdir -p /var/lib/reguant/logs
mkdir -p /var/lib/reguant/ssl
mkdir -p /etc/nginx/sites-enabled

# 5. Build Reguant Binary
echo -e "\n${BLUE}[3/5] Resolving Go packages and building Reguant...${NC}"
cd "$(dirname "$0")/.."
go mod tidy
go build -o /usr/local/bin/reguant cmd/reguant/main.go
echo -e "${GREEN}Reguant binary built and installed to /usr/local/bin/reguant.${NC}"

# 6. Configure Systemd Service for Reguant Daemon
echo -e "\n${BLUE}[4/5] Creating Systemd service for Reguant daemon...${NC}"
cat <<EOF > /etc/systemd/system/reguant.service
[Unit]
Description=Reguant GitOps PaaS Control Plane
After=network.target docker.service

[Service]
Type=simple
ExecStart=/usr/local/bin/reguant
Restart=always
RestartSec=5
WorkingDirectory=/var/lib/reguant
Environment=REGUANT_PORT=9000
Environment=REGUANT_DB_PATH=/var/lib/reguant/reguant.db
Environment=REGUANT_APPS_DIR=/var/lib/reguant/apps
Environment=REGUANT_LOGS_DIR=/var/lib/reguant/logs
Environment=REGUANT_NGINX_DIR=/etc/nginx/sites-enabled

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable reguant.service
systemctl restart reguant.service
echo -e "${GREEN}Reguant systemd service is active and running on port 9000.${NC}"

# 7. Configure Nginx for Dashboard API proxying
echo -e "\n${BLUE}[5/5] Hooking Reguant to Nginx proxy...${NC}"
cat <<EOF > /etc/nginx/sites-enabled/reguant-control-plane.conf
server {
    listen 80;
    server_name _; # Responds to all server IP requests or custom control domain

    # Proxy API & WebSocket requests to Go backend
    location /api/ {
        proxy_pass http://127.0.0.1:9000/api/;
        proxy_http_version 1.1;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection 'upgrade';
        proxy_set_header Host \$host;
        proxy_cache_bypass \$http_upgrade;
    }

    # Serve Static Dashboard SPA assets
    location / {
        root /var/lib/reguant/dashboard/dist;
        index index.html;
        try_files \$uri \$uri/ /index.html;
    }
}
EOF

# Restart Nginx
systemctl restart nginx
echo -e "${GREEN}Nginx proxy configured successfully.${NC}"

echo -e "\n${BLUE}===============================================${NC}"
echo -e "${GREEN}     Reguant successfully installed!           ${NC}"
echo -e "${GREEN}     API is running at http://YOUR_VPS_IP/api   ${NC}"
echo -e "${BLUE}===============================================${NC}"
