FROM scratch

COPY tugrik /tugrik

ADD https://raw.githubusercontent.com/ConnectCorp/docker-scratch-ssl/master/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt


CMD ["/tugrik"]