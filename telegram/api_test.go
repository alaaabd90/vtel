package telegram

// Compile-time check that both transports satisfy the shared API contract
// Sender/Poller/tunnel.LinkSpec depend on.
var (
	_ API = (*BotAPI)(nil)
	_ API = (*AccountAPI)(nil)
)
