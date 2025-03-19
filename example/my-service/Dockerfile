FROM golang
WORKDIR /app
COPY . .
RUN go build -o app main.go
ENTRYPOINT [ "/app/app" ]