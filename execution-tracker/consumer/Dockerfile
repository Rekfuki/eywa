FROM scratch

COPY execution-tracker-consumer /execution-tracker-consumer

ADD https://raw.githubusercontent.com/ConnectCorp/docker-scratch-ssl/master/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt


CMD ["/execution-tracker-consumer"]