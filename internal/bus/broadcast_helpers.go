package bus

// Broadcast emits a named event to all connected WebSocket clients.
func Broadcast(pub EventPublisher, name string, payload any) {
	pub.Broadcast(Event{Name: name, Payload: payload})
}
