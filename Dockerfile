FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -tags nodynamic -trimpath -ldflags="-s -w" -o /leopic .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /leopic /leopic
USER 65532:65532
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/leopic"]
CMD ["serve"]
