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

# Prompt for SSL email address for Let's Encrypt certificates
if [ -z "${REGUANT_SSL_EMAIL:-}" ]; then
  if [ -t 0 ]; then
    read -p "Enter your SSL email address (for Let's Encrypt notifications): " REGUANT_SSL_EMAIL
    while [ -z "$REGUANT_SSL_EMAIL" ]; do
      echo -e "${RED}Email address cannot be empty.${NC}"
      read -p "Enter your SSL email address (for Let's Encrypt notifications): " REGUANT_SSL_EMAIL
    done
  else
    # Try reading from /dev/tty if running in a piped context
    if read -p "Enter your SSL email address (for Let's Encrypt notifications): " REGUANT_SSL_EMAIL < /dev/tty 2>/dev/null; then
      while [ -z "$REGUANT_SSL_EMAIL" ]; do
        echo -e "${RED}Email address cannot be empty.${NC}"
        read -p "Enter your SSL email address: " REGUANT_SSL_EMAIL < /dev/tty
      done
    else
      REGUANT_SSL_EMAIL="admin@localhost"
      echo -e "${BLUE}Non-interactive terminal detected; defaulting SSL email to $REGUANT_SSL_EMAIL${NC}"
    fi
  fi
fi

# Distro and Package Manager Detection
if command -v apt-get &> /dev/null; then
  PKG_MANAGER="apt"
elif command -v dnf &> /dev/null; then
  PKG_MANAGER="dnf"
elif command -v yum &> /dev/null; then
  PKG_MANAGER="yum"
elif command -v apk &> /dev/null; then
  PKG_MANAGER="apk"
else
  echo -e "${RED}Error: Unsupported distribution (no apt, dnf, yum, or apk found).${NC}"
  exit 1
fi

echo -e "${GREEN}Detected package manager: $PKG_MANAGER${NC}"

# 1. Update Packages & Dependencies
echo -e "\n${BLUE}[1/5] Installing core server dependencies...${NC}"
if [ "$PKG_MANAGER" = "apt" ]; then
  apt-get update
  apt-get install -y curl git wget build-essential nginx certbot python3-certbot-nginx jq
elif [ "$PKG_MANAGER" = "dnf" ] || [ "$PKG_MANAGER" = "yum" ]; then
  $PKG_MANAGER install -y epel-release || true
  $PKG_MANAGER update -y
  $PKG_MANAGER install -y curl git wget make gcc nginx certbot python3-certbot-nginx jq
elif [ "$PKG_MANAGER" = "apk" ]; then
  apk update
  apk add curl git wget build-base nginx certbot certbot-nginx jq bash
fi

# Enable auto-renewing certbot SSL timer if systemd is active
if command -v systemctl &> /dev/null && systemctl is-active dbus &> /dev/null; then
  systemctl enable certbot.timer || true
  systemctl start certbot.timer || true
fi

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
  GO_VERSION="1.25.0"
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

# Create the unprivileged runtime user that deployed applications run as
if ! id -u reguant-apps >/dev/null 2>&1; then
  if command -v useradd &> /dev/null; then
    useradd --system --no-create-home --shell /usr/sbin/nologin reguant-apps
  else
    adduser -S -D -H -s /sbin/nologin reguant-apps
  fi
fi
chown -R reguant-apps:reguant-apps /var/lib/reguant/apps /var/lib/reguant/logs

# 5. Build Reguant Binary
echo -e "\n${BLUE}[3/5] Resolving Go packages and building Reguant...${NC}"
# Resolve repository directory (works for cloned checkouts and piped installs)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
# When installed via the curl pipe there is no local checkout; clone the source.
if [ ! -f "$REPO_DIR/go.mod" ]; then
  echo -e "${BLUE}Source not found locally; cloning Reguant repository...${NC}"
  CLONE_TMP="$(mktemp -d)"
  git clone --depth 1 https://github.com/shahrryyar/Reguant.git "$CLONE_TMP/Reguant"
  REPO_DIR="$CLONE_TMP/Reguant"
fi
cd "$REPO_DIR"
go mod tidy
go build -o /usr/local/bin/reguant cmd/reguant/main.go
echo -e "${GREEN}Reguant binary built and installed to /usr/local/bin/reguant.${NC}"

# Deploy the dashboard SPA assets served by Nginx in production
mkdir -p /var/lib/reguant/dashboard
cp -r "$REPO_DIR/dashboard/dist/." /var/lib/reguant/dashboard/dist/ 2>/dev/null \
  || echo -e "${BLUE}Note: dashboard/dist not present; build the dashboard or serve in dev mode.${NC}"

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
Environment=REGUANT_SSL_EMAIL=$REGUANT_SSL_EMAIL

[Install]
WantedBy=multi-user.target
EOF

if command -v systemctl &> /dev/null && systemctl is-active dbus &> /dev/null; then
  systemctl daemon-reload
  systemctl enable reguant.service
  systemctl restart reguant.service
  echo -e "${GREEN}Reguant systemd service is active and running on port 9000.${NC}"
else
  echo -e "${BLUE}Note: systemd is not active or available. Skipping service registration.${NC}"
  echo -e "${BLUE}You can run the daemon manually: REGUANT_SSL_EMAIL=$REGUANT_SSL_EMAIL /usr/local/bin/reguant${NC}"
fi

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
if command -v systemctl &> /dev/null && systemctl is-active dbus &> /dev/null; then
  systemctl restart nginx
elif command -v rc-service &> /dev/null; then
  rc-service nginx restart || true
else
  nginx -s reload || nginx || true
fi
echo -e "${GREEN}Nginx proxy configured successfully.${NC}"

echo -e "\n${BLUE}===============================================${NC}"
echo -e "${GREEN}     Reguant successfully installed!           ${NC}"
echo -e "${GREEN}     API is running at http://YOUR_VPS_IP/api   ${NC}"
echo -e "${BLUE}===============================================${NC}"
