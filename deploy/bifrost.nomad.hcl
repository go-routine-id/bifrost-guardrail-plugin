## bifrost — AI gateway with the built-in guardrail plugin.
## Single-node pattern: no CNI bridge, container uses host networking, the app listens on
## loopback and a host-level reverse proxy exposes it. Secrets come from Nomad Variables.
##
## Deployed by CI: `nomad job run -var "image=ghcr.io/<org>/bifrost:<tag>" bifrost.nomad.hcl`

variable "image" {
  type        = string
  description = "Full image reference to deploy (e.g. ghcr.io/<org>/bifrost:1.6.0-guardrail)"
}

variable "app_port" {
  type    = number
  default = 8889
}

variable "volume_source" {
  type    = string
  default = "llm-bifrost-data"
}

variable "nomad_var_path" {
  type    = string
  default = "nomad/jobs/bifrost"
}

job "bifrost" {
  datacenters = ["dc1"]
  type        = "service"

  group "bifrost" {
    count = 1

    network {
      port "http" {
        static       = var.app_port
        host_network = "loopback"
      }
    }

    volume "data" {
      type      = "host"
      source    = var.volume_source
      read_only = false
    }

    service {
      name     = "bifrost"
      port     = "http"
      provider = "nomad"
      tags     = ["llm", "bifrost", "gateway", "production"]

      check {
        type     = "http"
        path     = "/health"
        interval = "15s"
        timeout  = "3s"
      }
    }

    restart {
      attempts = 3
      interval = "5m"
      delay    = "15s"
      mode     = "delay"
    }

    task "bifrost" {
      driver = "docker"

      config {
        image        = var.image
        network_mode = "host"

        # Pull a private GHCR image. Credentials are interpolated from env vars
        # populated by the template below — no credentials live in this job spec.
        auth {
          username = "${GHCR_USER}"
          password = "${GHCR_PASS}"
        }
      }

      # Exposes the GHCR pull credentials as env so config.auth can use them.
      # ghcr_user / ghcr_pass come from Nomad Variables (pass = a PAT or gh token
      # with read:packages). Stored at var.nomad_var_path.
      template {
        destination = "secrets/ghcr.env"
        env         = true
        data        = <<EOH
{{ with nomadVar "${var.nomad_var_path}" }}
GHCR_USER={{ .ghcr_user }}
GHCR_PASS={{ .ghcr_pass }}
{{ end }}
EOH
      }

      env {
        APP_PORT = "${var.app_port}"
        APP_HOST = "127.0.0.1"
      }

      volume_mount {
        volume      = "data"
        destination = "/app/data"
      }

      # Secrets from Nomad Variables -> env. config.json reads them via "env.*".
      # The encryption key must stay constant (encrypts virtual keys in config.db).
      template {
        destination = "secrets/app.env"
        env         = true
        data        = <<EOH
{{ with nomadVar "${var.nomad_var_path}" }}
KIMI_API_KEY={{ .kimi_api_key }}
KIMI_API_KEY_2={{ .kimi_api_key_2 }}
BIFROST_ENCRYPTION_KEY={{ .bifrost_encryption_key }}
{{ end }}
EOH
      }

      resources {
        cpu    = 1000
        memory = 1024
      }
    }
  }
}
