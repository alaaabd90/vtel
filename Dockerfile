FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod .
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /vtel ./cmd/vtel

FROM alpine
RUN apk add --no-cache ca-certificates
COPY --from=build /vtel /vtel
ENTRYPOINT ["/vtel"]
