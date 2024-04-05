FROM intel/intel-optimized-ffmpeg:latest
COPY start.sh healthy.sh cmd/* /app/
RUN mkdir -p /downloads /config \
    && apt-get update \
    && apt-get install -y curl gosu \
    && ln -s /opt/build/bin/ffmpeg /usr/bin/ffmpeg
ENV YOUTUBEDR=/app/youtubedr \
    LISTEN_HOST="0.0.0.0" \
    LISTEN_PORT="9270" \
    DOWNLOAD="/downloads" \
    PUID=1000 \
    PGID=1000
RUN chmod +x /app/youtubedr \
    && chmod +x /app/youtubedr-web \
    && chmod +x /app/start.sh \
    && chmod +x /app/healthy.sh \
    && useradd -d /app -u ${PUID} -M -U user
EXPOSE 9270
WORKDIR /app
HEALTHCHECK --interval=30s --timeout=30s --start-period=5s --retries=3 CMD [ "/app/healthy.sh" ]
LABEL version="2.10.1"
ENTRYPOINT ["/app/start.sh"]
LABEL youtubedr-version="2.10.1"
LABEL author="hanke and hacked ugly by gansui from https://github.com/hanke0/bbdown-web"
