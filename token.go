package activedns

// embeddedToken is the token used when [WithToken] is not given. It is public:
// it identifies requests as coming from this SDK, and the server limits it
// per client network address.
//
// A build can replace it:
//
//	go build -ldflags "-X github.com/activedns/activedns-go.embeddedToken=<token>"
var embeddedToken = "2eb6a731-fe04-40aa-8c03-2c643849a659"
