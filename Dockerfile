FROM golang:1.21-alpine
WORKDIR /app
COPY . .
RUN go build -o bot main.go
ENV PORT=8080
EXPOSE 8080
CMD ["./bot"]
