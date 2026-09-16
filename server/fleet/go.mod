module robot-agent/fleet

go 1.25

require (
	github.com/eclipse/paho.mqtt.golang v1.5.1
	github.com/gorilla/websocket v1.5.3
	github.com/lib/pq v1.12.3
	google.golang.org/protobuf v1.36.12
	robot-agent/protocols v0.0.0-00010101000000-000000000000
)

replace robot-agent/protocols => ../../protocols

require (
	golang.org/x/net v0.44.0 // indirect
	golang.org/x/sync v0.17.0 // indirect
)
