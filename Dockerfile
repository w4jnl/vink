# Built by goreleaser (dockers_v2): the context holds one prebuilt binary per
# platform under linux/<arch>/, nothing is compiled here. FROM scratch, so the
# image holds the static binary and CA certificates.
FROM alpine:3.20 AS certs
RUN apk add --no-cache ca-certificates

FROM scratch
ARG TARGETPLATFORM
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY $TARGETPLATFORM/vink /vink
ENV VINK_DB_PATH=/data/vink.db
ENV VINK_SECRETS_KEY_FILE=/data/secret.key
ENV VINK_SERVER_LISTEN=:8080
VOLUME ["/data"]
EXPOSE 8080
# The image has no shell: `docker run … ghcr.io/w4jnl/vink agent --server …` runs a probe agent,
# `docker compose exec vink /vink admin init …` bootstraps.
ENTRYPOINT ["/vink"]
CMD ["serve"]
