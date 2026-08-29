ARG NODE_VERSION=22-alpine
ARG GO_VERSION=1.26.3-alpine
ARG GRAFANA_VERSION=12.3.3

FROM node:${NODE_VERSION} AS frontend
WORKDIR /src
COPY package.json package-lock.json* ./
RUN npm ci
COPY . .
RUN npm run build

FROM golang:${GO_VERSION} AS backend
ARG MCP_GRAFANA_VERSION=v1.2.0
WORKDIR /src
COPY go.mod go.sum Magefile.go ./
RUN go mod download
RUN GOBIN=/out go install github.com/grafana/mcp-grafana/cmd/mcp-grafana@${MCP_GRAFANA_VERSION}
COPY pkg ./pkg
COPY src/plugin.json ./src/plugin.json
COPY --from=frontend /src/dist ./dist
RUN go run github.com/magefile/mage@v1.15.0 -v build:linux build:linuxARM64

FROM grafana/grafana:${GRAFANA_VERSION}
USER root
# Keep the plugin outside /var/lib/grafana: that path is a named volume, and Docker
# only seeds a volume from the image while it is still empty. Shipping the plugin
# there means every later rebuild is silently ignored and Grafana keeps loading a
# stale binary.
ENV GF_PATHS_PLUGINS=/opt/grafana-plugins
COPY --from=backend /out/mcp-grafana /usr/local/bin/mcp-grafana
COPY --from=backend /src/dist /opt/grafana-plugins/acme-aiassistant-app
RUN chmod +x /usr/local/bin/mcp-grafana /opt/grafana-plugins/acme-aiassistant-app/gpx_ai_assistant_linux_* \
    && chown -R grafana:root /opt/grafana-plugins
USER grafana
