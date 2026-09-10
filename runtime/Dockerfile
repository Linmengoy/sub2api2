FROM alpine:3.23
RUN apk add --no-cache ca-certificates tzdata su-exec curl wget postgresql18-client && rm -rf /var/cache/apk/*
RUN addgroup -g 1000 sub2api && adduser -u 1000 -G sub2api -s /bin/sh -D sub2api
WORKDIR /app
COPY sub2api /app/sub2api
COPY docker-entrypoint.sh /app/docker-entrypoint.sh
COPY resources /app/resources
RUN chmod +x /app/docker-entrypoint.sh && mkdir -p /app/data && chown -R sub2api:sub2api /app
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=10s --start-period=10s --retries=3 CMD wget -q -T 5 -O /dev/null http://localhost:8080/health || exit 1
ENTRYPOINT ["/app/docker-entrypoint.sh"]
CMD ["/app/sub2api"]
