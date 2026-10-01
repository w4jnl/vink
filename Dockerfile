# Built by goreleaser: the binary is copied in, nothing is compiled here.
# FROM scratch, so the image holds the static binary and CA certificates.
FROM alpine:3.20 AS certs
RUN apk add --no-cache ca-certificates

FROM scratch
LABEL org.opencontainers.image.title="vink" \
      org.opencontainers.image.description="Self-hosted heartbeat and uptime monitor; the same image runs the server, the probe agent and the CLI" \
      org.opencontainers.image.source="https://github.com/w4jnl/vink"
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY vink /vink
ENV VINK_DB_PATH=/data/vink.db
ENV VINK_SECRETS_KEY_FILE=/data/secret.key
ENV VINK_SERVER_LISTEN=:8080
VOLUME ["/data"]
EXPOSE 8080
# The image has no shell: `docker run … ghcr.io/w4jnl/vink agent --server …` runs a probe agent,
# `docker compose exec vink /vink admin init …` bootstraps.
ENTRYPOINT ["/vink"]
CMD ["serve"]
