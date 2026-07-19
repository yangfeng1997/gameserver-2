package main

import "testing"

func TestLintMethodName(t *testing.T) {
	tests := []struct {
		name        string
		methodName  string
		inputName   string
		outputName  string
		notify      bool
		kind        serviceKind
		expectError bool
	}{
		{"valid Notify", "TestNtf", "RPC_Test_Ntf", "", true, kindBackend, false},
		{"valid Req/Rsp FRONTEND", "Ping", "CS_Ping_Req", "SC_Pong_Rsp", false, kindFrontend, false},
		{"valid Req/Rsp BACKEND", "Test", "RPC_Test_Req", "RPC_Test_Rsp", false, kindBackend, false},
		{"Notify missing _Ntf", "TestNtf", "RPC_Test_Req", "", true, kindBackend, true},
		{"Req missing _Req", "Ping", "CS_Ping", "SC_Pong_Rsp", false, kindFrontend, true},
		{"Rsp missing _Rsp", "Ping", "CS_Ping_Req", "SC_Pong", false, kindFrontend, true},
		{"FRONTEND input _Rsp rejected", "Foo", "SC_Foo_Rsp", "CS_Foo_Req", false, kindFrontend, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := lintMethodName(tt.methodName, tt.inputName, tt.outputName, tt.notify, tt.kind)
			if (err != nil) != tt.expectError {
				t.Errorf("error=%v, expectError=%v", err, tt.expectError)
			}
		})
	}
}

func TestServerTypeToVal(t *testing.T) {
	tests := map[string]string{
		"ST_GATESVR": "1", "ST_LOBBYSVR": "2", "ST_ROUTERAGENT": "6",
	}
	for input, expected := range tests {
		if got := serverTypeToVal(input); got != expected {
			t.Errorf("serverTypeToVal(%q)=%q, want %q", input, got, expected)
		}
	}
}

func TestRouteConstName(t *testing.T) {
	if got := routeConstName("LobbyHandler", "Ping"); got != "RouteLobbyHandlerPing" {
		t.Fatalf("got %q, want RouteLobbyHandlerPing", got)
	}
}

func TestToSnake(t *testing.T) {
	tests := map[string]string{
		"Lobby": "lobby", "Online": "online", "FooBar": "foo_bar", "FooBARBaz": "foo_bar_baz",
	}
	for input, expected := range tests {
		if got := toSnake(input); got != expected {
			t.Errorf("toSnake(%q)=%q, want %q", input, got, expected)
		}
	}
}

func TestLastSeg(t *testing.T) {
	tests := map[string]string{
		"project/protocol/handler": "handler",
		"no_slash":                 "no_slash",
	}
	for input, expected := range tests {
		if got := lastSeg(input); got != expected {
			t.Errorf("lastSeg(%q)=%q, want %q", input, got, expected)
		}
	}
}
