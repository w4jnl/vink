# Built by goreleaser: the binary is copied in, nothing is compiled here.
# FROM scratch, so the image holds the static binary and CA certificates.
FROM alpine:3.20 AS certs
RUN apk add --no-cache ca-certificates

FROM scratch
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY vink /vink
ENV VINK_DB_PATH=/data/vink.db
ENV VINK_SERVER_LISTEN=:8080
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/vink"]
CMD ["serve"]
