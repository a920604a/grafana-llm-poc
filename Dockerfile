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
COPY pkg ./pkg
COPY src/plugin.json ./src/plugin.json
COPY --from=frontend /src/dist ./dist
RUN go run github.com/magefile/mage@v1.15.0 -v build:linux build:linuxARM64
RUN GOBIN=/out go install github.com/grafana/mcp-grafana/cmd/mcp-grafana@${MCP_GRAFANA_VERSION}

FROM grafana/grafana:${GRAFANA_VERSION}
USER root
COPY --from=backend /out/mcp-grafana /usr/local/bin/mcp-grafana
COPY --from=backend /src/dist /var/lib/grafana/plugins/acme-aiassistant-app
RUN chmod +x /usr/local/bin/mcp-grafana /var/lib/grafana/plugins/acme-aiassistant-app/gpx_ai_assistant_linux_*
USER grafana
