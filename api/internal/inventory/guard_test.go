package inventory

import "testing"

func TestVersionLabelIsNotGoversion(t *testing.T) {
	cases := map[string]string{
		`node_exporter_build_info{branch="HEAD",goarch="amd64",goos="linux",goversion="go1.23.4",revision="abc",tags="unknown",version="1.9.1"} 1`: "1.9.1",
		`node_exporter_build_info{version="1.8.2",goversion="go1.22"} 1`:                                                                           "1.8.2",
		// A source build with an empty version must not report the Go version.
		`node_exporter_build_info{branch="",goarch="amd64",goos="linux",goversion="go1.27.1",revision="6044da7",tags="unknown",version=""} 1`: "",
	}
	for line, want := range cases {
		got := ""
		if m := versionRe.FindStringSubmatch(line); m != nil {
			got = m[1]
		}
		if got != want {
			t.Errorf("version = %q, want %q\n  in %s", got, want, line)
		}
	}
}
