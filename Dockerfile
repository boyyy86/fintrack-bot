FROM golang:1.21
WORKDIR /app
COPY . .
RUN go mod download
RUN go build -o bot main.go
ENV PORT=8080
EXPOSE 8080
CMD ["./bot"]