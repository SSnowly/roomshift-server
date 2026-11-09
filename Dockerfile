FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download || true
COPY . .
RUN go mod tidy && CGO_ENABLED=0 go build -o /roomshift-server .

FROM alpine:3.20
COPY --from=build /roomshift-server /roomshift-server
EXPOSE 8765
ENTRYPOINT ["/roomshift-server", "-addr", ":8765"]
