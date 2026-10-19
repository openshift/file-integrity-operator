package framework

import "testing"

// Not wired into `make test-unit` (which excludes /tests), so exercised as
// a plain `go test` target: a wrong parse here would silently turn
// AssertMetricsEndpointRejectsTLSVersion into a vacuous pass.
func TestParseRejectTestExitCode(t *testing.T) {
	cases := []struct {
		name       string
		output     string
		wantCode   string
		wantParsed bool
	}{
		{
			name:       "genuine TLS rejection",
			output:     "* TLSv1.2 (OUT), TLS handshake, Client hello (1):\ncurl: (35) OpenSSL error\nREJECT_TEST_EXIT:35",
			wantCode:   "35",
			wantParsed: true,
		},
		{
			name:       "server wrongly accepted the capped handshake",
			output:     "* SSL connection using TLSv1.2 / ECDHE-RSA-AES128-GCM-SHA256\nREJECT_TEST_EXIT:0",
			wantCode:   "0",
			wantParsed: true,
		},
		{
			name:       "oc run never got far enough to run curl",
			output:     `Error from server (AlreadyExists): pods "tls-reject-test" already exists`,
			wantParsed: false,
		},
		{
			name:       "empty output",
			output:     "",
			wantParsed: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, ok := parseRejectTestExitCode(tc.output)
			if ok != tc.wantParsed {
				t.Fatalf("ok = %v, want %v", ok, tc.wantParsed)
			}
			if ok && code != tc.wantCode {
				t.Fatalf("code = %q, want %q", code, tc.wantCode)
			}
		})
	}
}
