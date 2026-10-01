# Nomad job for vink: one allocation, the SQLite file on a host volume,
# Traefik tags for the open and the authenticated routes (see
# traefik-authelia.yaml for the middlewares). Health is /readyz.
#
#   nomad volume: a host_volume "vink" on the client, e.g. /opt/nomad/vink
#   nomad var put nomad/jobs/vink proxy_secret=…   (or set the env another way)

job "vink" {
  datacenters = ["dc1"]
  type        = "service"

  group "vink" {
    count = 1

    volume "data" {
      type   = "host"
      source = "vink"
    }

    network {
      port "http" { to = 8080 }
    }

    service {
      name     = "vink"
      port     = "http"
      provider = "nomad"
      tags = [
        "traefik.enable=true",
        "traefik.http.routers.vink-ui.rule=Host(`vink.w4j.nl`)",
        "traefik.http.routers.vink-ui.entrypoints=websecure",
        "traefik.http.routers.vink-ui.middlewares=authelia@file,vink-secret@file",
        "traefik.http.routers.vink-open.rule=Host(`vink.w4j.nl`) && (PathPrefix(`/ping/`) || PathPrefix(`/api/`) || PathPrefix(`/s/`) || PathPrefix(`/agent/`) || Path(`/metrics`) || Path(`/healthz`) || Path(`/readyz`))",
        "traefik.http.routers.vink-open.priority=100",
        "traefik.http.routers.vink-open.entrypoints=websecure",
        "traefik.http.routers.vink-open.middlewares=strip-identity@file,vink-secret@file",
      ]
      check {
        type     = "http"
        path     = "/readyz"
        interval = "15s"
        timeout  = "3s"
      }
    }

    task "vink" {
      driver = "docker"

      config {
        image = "ghcr.io/w4jnl/vink:latest"
        ports = ["http"]
        args  = ["serve"]
        # icmp monitors: cap_add = ["NET_RAW"]
      }

      volume_mount {
        volume      = "data"
        destination = "/data"
      }

      env {
        VINK_SERVER_LISTEN        = ":8080"
        VINK_SERVER_BASE_URL      = "https://vink.w4j.nl"
        VINK_SERVER_TRUSTED_PROXIES = "10.0.0.0/8"
        VINK_DB_PATH              = "/data/vink.db"
        VINK_SECRETS_KEY_FILE     = "/data/secret.key"
        VINK_LOG_FORMAT           = "json"
        VINK_AUTH_PROXY_ENABLED   = "true"
        VINK_AUTH_PROXY_TRUSTED_CIDRS = "10.0.0.0/8"
        VINK_AUTH_PROXY_SECRET    = "env:VINK_PROXY_SECRET"
      }

      template {
        data        = <<-EOT
          VINK_PROXY_SECRET={{ with nomadVar "nomad/jobs/vink" }}{{ .proxy_secret }}{{ end }}
        EOT
        destination = "secrets/vink.env"
        env         = true
      }

      resources {
        cpu    = 200
        memory = 128
      }

      kill_timeout = "15s"
    }
  }
}
